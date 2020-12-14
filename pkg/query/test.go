// Copyright (c) The Thanos Authors.
// Licensed under the Apache License 2.0.

package query

import (
	"context"
	"io/ioutil"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/teststorage"
)

var (
	patSpace    = regexp.MustCompile("[\t ]+")
	patSetStore = regexp.MustCompile(`^set\s+([a-zA-Z0-9]+)\s+([0-9]+)\s+([0-9]+?)$`)
)

func durationMilliseconds(d time.Duration) int64 {
	return int64(d / (time.Millisecond / time.Nanosecond))
}

type test struct {
	testing.TB

	cmds       []interface{}
	rootEngine *promql.Engine
	stores     []*testStore

	ctx       context.Context
	cancelCtx context.CancelFunc
}

type testStore struct {
	storage *teststorage.TestStorage

	ctx       context.Context
	cancelCtx context.CancelFunc
}

func (s *testStore) reset(t testing.TB) {
	if s.cancelCtx != nil {
		s.cancelCtx()
	}

	if s.storage != nil {
		if err := s.storage.Close(); err != nil {
			t.Fatalf("closing test storage: %s", err)
		}
	}
	s.storage = teststorage.New(t)
	s.ctx, s.cancelCtx = context.WithCancel(context.Background())
}

// close closes resources associated with the testStore.
func (s *testStore) close(t testing.TB) {
	s.cancelCtx()

	if err := s.storage.Close(); err != nil {
		t.Fatalf("closing test storage: %s", err)
	}
}

// NewTest returns an initialized empty Test.
// It's similar to promql.Test, just supporting multi StoreAPIs allowing for query pushdown testing.
func newTest(t testing.TB, input string) (*test, error) {
	cmds, err := parse(input)
	if err != nil {
		return nil, err
	}

	te := &test{TB: t, cmds: cmds}

	te.reset()
	return te, err
}

func newTestFromFile(t testing.TB, filename string) (*test, error) {
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	return newTest(t, string(content))
}

// reset the current test storage of all inserted samples.
func (t *test) reset() {
	if t.cancelCtx != nil {
		t.cancelCtx()
	}
	t.ctx, t.cancelCtx = context.WithCancel(context.Background())

	opts := promql.EngineOpts{
		Logger:                   nil,
		Reg:                      nil,
		MaxSamples:               10000,
		Timeout:                  100 * time.Second,
		NoStepSubqueryIntervalFn: func(int64) int64 { return durationMilliseconds(1 * time.Minute) },
	}
	t.rootEngine = promql.NewEngine(opts)

	for _, s := range t.stores {
		s.reset(t.TB)
	}
}

// close closes resources associated with the Test.
func (t *test) close() {
	t.cancelCtx()
	for _, s := range t.stores {
		s.close(t.TB)
	}
}

// getLines returns trimmed lines after removing the comments.
func getLines(input string) []string {
	lines := strings.Split(input, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			l = ""
		}
		lines[i] = l
	}
	return lines
}

// parse parses the given input and returns command sequence.
func parse(input string) (cmds []interface{}, err error) {
	lines := getLines(input)

	// Scan for steps line by line.
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if len(l) == 0 {
			continue
		}
		var cmd interface{}

		switch c := strings.ToLower(patSpace.Split(l, 2)[0]); {
		case c == "clear":
			cmd = &promql.ClearCmd{}
		case c == "load":
			i, cmd, err = promql.ParseLoad(lines, i)
		case strings.HasPrefix(c, "eval"):
			i, cmd, err = promql.ParseEval(lines, i)
		case c == "set":
			i, cmd, err = ParseSetStore(lines, i)
		default:
			return nil, raise(i, "invalid command %q", l)
		}
		if err != nil {
			return nil, err
		}
		cmds = append(cmds, cmd)
	}
	return cmds, nil
}

func raise(line int, format string, v ...interface{}) error {
	return &parser.ParseErr{
		LineOffset: line,
		Err:        errors.Errorf(format, v...),
	}
}

// run executes the command sequence of the test. Until the maximum error number
// is reached, evaluation errors do not terminate execution.
func (t *test) run() error {
	for _, cmd := range t.cmds {
		if err := t.exec(cmd); err != nil {
			return err
		}
	}
	return nil
}

// exec processes a single step of the test.
func (t *test) exec(tc interface{}, createQueryableFn func() storage.Queryable) error {
	switch cmd := tc.(type) {
	case *promql.ClearCmd:
		t.reset()
	case *setStoreCmd:
		s := &testStore{}
		s.reset(t.TB)
		t.stores = append(t.stores, s)

	case *promql.LoadCmd:
		if len(t.stores) == 0 {
			s := &testStore{}
			s.reset(t.TB)
			t.stores = append(t.stores, s)
		}

		app := t.stores[len(t.stores)-1].storage.Appender(t.ctx)
		if err := cmd.Append(app); err != nil {
			app.Rollback()
			return err
		}
		if err := app.Commit(); err != nil {
			return err
		}

	case *promql.EvalCmd:
		if err := cmd.Eval(t.ctx, t.queryEngine, t.storage); err != nil {
			return err
		}

	default:
		panic("promql.Test.exec: unknown test command type")
	}
	return nil
}

// setStoreCmd is a command that appends new storage with filter.
type setStoreCmd struct {
	mint, maxt int
	matchers   []*labels.Selector
}

func newSetStoreCmd(mint, maxt int, matchers []*labels.Selector) *setStoreCmd {
	return &setStoreCmd{
		mint:     mint,
		maxt:     maxt,
		matchers: matchers,
	}
}

func (cmd setStoreCmd) String() string {
	return "set"
}

// ParseLoad parses load statements.
func ParseSetStore(lines []string, i int) (int, *setStoreCmd, error) {
	if !patSetStore.MatchString(lines[i]) {
		return i, nil, raise(i, "invalid set command. (set <matchers> <mint> <maxt?>")
	}
	parts := patSetStore.FindStringSubmatch(lines[i])

	gap, err := model.ParseDuration(parts[1])
	if err != nil {
		return i, nil, raise(i, "invalid step definition %q: %s", parts[1], err)
	}
	cmd := newSetStoreCmd(time.Duration(gap))
	for i+1 < len(lines) {
		i++
		defLine := lines[i]
		if len(defLine) == 0 {
			i--
			break
		}
		metric, vals, err := parser.ParseSeriesDesc(defLine)
		if err != nil {
			if perr, ok := err.(*parser.ParseErr); ok {
				perr.LineOffset = i
			}
			return i, nil, err
		}
		cmd.set(metric, vals...)
	}
	return i, cmd, nil
}
