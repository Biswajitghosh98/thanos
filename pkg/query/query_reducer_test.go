package query

import (
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/prometheus/promql"
	"github.com/stretchr/testify/require"
	"github.com/thanos-io/thanos/pkg/testutil"
)

func TestQuerierWithMapReducing_PromQL(t *testing.T) {
	promPath, err := exec.Command("go", "list", "-f", "{{ .Dir }}", "-m", "github.com/prometheus/prometheus").Output()
	testutil.Ok(t, err)

	files, err := filepath.Glob(filepath.Join(filepath.Join(strings.TrimRight(string(promPath), "\n"), "promql", "testdata", "*.test")))
	testutil.Ok(t, err)
	testutil.Equals(t, 9, len(files))

	for _, fn := range files {
		t.Run(fn, func(t *testing.T) {
			content, err := ioutil.ReadFile(fn)
			testutil.Ok(t, err)
			require.NoError(t, err)

			test, err := promql.NewTest(t, string(content))
			testutil.Ok(t, err)

			test.
				testutil.Ok(t, test.Run())
			test.Close()
		})
	}

}

/**
// TODO(bwplotka): Move to separate package?
func TestQuerierWithMapReducing(t *testing.T) {
	logger := log.NewLogfmtLogger(os.Stderr)
	timeout := 100 * time.Second

	// Readable example of extemely shared store APIs and their responses.
	// NOTE: Series are sorted within label sets. Within series samples are sorted by time. This is all by StoreAPI contract.
	// Non overlapping samples.
	storeResponsesShardedBySomeSeriesByTime := [][][]*storepb.SeriesResponse{
		// Vertical shards (series).
		{
			// Horizontal shards (time).
			{
				storeSeriesResponse(t, labels.FromStrings("_type", "counter", "a", "a"), []sample{{0, 0}, {2, 3}, {3, 51}}),
				storeSeriesResponse(t, labels.FromStrings("_type", "counter", "a", "a"), []sample{{5, 65}, {6, 24}, {7, 321}}), // Reset.
				storeSeriesResponse(t, labels.FromStrings("_type", "counter", "a", "b"), []sample{{2, 4}, {3, 24}, {4, 33}}, []sample{{5, 77}, {6, 94}, {7, 360}}),
				storeSeriesResponse(t, labels.FromStrings("_type", "counter", "a", "c"), []sample{{100, 1}, {300, 3}, {400, 54}}),

				storeSeriesResponse(t, labels.FromStrings("_type", "gauge", "a", "a"), []sample{{0, -24}, {2, 53}, {3, -2}}),
				storeSeriesResponse(t, labels.FromStrings("_type", "gauge", "a", "a"), []sample{{5, 10}, {6, 23}, {7, -30}}),
				storeSeriesResponse(t, labels.FromStrings("_type", "gauge", "a", "b"), []sample{{2, 24}, {3, -12}, {4, 4}}, []sample{{5, 12}, {6, 45}, {7, -24}}),
				storeSeriesResponse(t, labels.FromStrings("_type", "gauge", "a", "c"), []sample{{100, 1}, {300, -3}, {400, 43}}),
			},
			{
				storeSeriesResponse(t, labels.FromStrings("a", "a"), []sample{{0, 0}, {2, 1}, {3, 2}}),
				storeSeriesResponse(t, labels.FromStrings("a", "a"), []sample{{5, 5}, {6, 6}, {7, 7}}),
				storeSeriesResponse(t, labels.FromStrings("a", "a"), []sample{{5, 5}, {6, 66}}), // Overlap samples for some reason.
				storeSeriesResponse(t, labels.FromStrings("a", "b"), []sample{{2, 2}, {3, 3}, {4, 4}}, []sample{{1, 1}, {2, 2}, {3, 3}}),
				storeSeriesResponse(t, labels.FromStrings("a", "c"), []sample{{100, 1}, {300, 3}, {400, 4}}),
			},
		},
	}


	t.Run("group by", func(t *testing.T) {
		for _, c := range []struct {
			query string
			storeAPI storepb.StoreServer

			mint, maxt      int64
			matchers        []*labels.Matcher
			replicaLabels   []string
			hints           *storage.SelectHints
			equivalentQuery string

			expected           []series
			expectedAfterDedup series
			expectedWarning    string
		}{
			{
				name: "select overlapping data with partial error",
				storeAPI: &testStoreServer{
					resps: []*storepb.SeriesResponse{
						storeSeriesResponse(t, labels.FromStrings("a", "a"), []sample{{0, 0}, {2, 1}, {3, 2}}),
						storepb.NewWarnSeriesResponse(errors.New("partial error")),
						storeSeriesResponse(t, labels.FromStrings("a", "a"), []sample{{5, 5}, {6, 6}, {7, 7}}),
						storeSeriesResponse(t, labels.FromStrings("a", "a"), []sample{{5, 5}, {6, 66}}), // Overlap samples for some reason.
						storeSeriesResponse(t, labels.FromStrings("a", "b"), []sample{{2, 2}, {3, 3}, {4, 4}}, []sample{{1, 1}, {2, 2}, {3, 3}}),
						storeSeriesResponse(t, labels.FromStrings("a", "c"), []sample{{100, 1}, {300, 3}, {400, 4}}),
					},
				},
				mint: 1, maxt: 300,
				replicaLabels:   []string{"a"},
				equivalentQuery: `{a=~"a|b|c"}`,

				expected: []series{
					{
						lset:    labels.FromStrings("a", "a"),
						samples: []sample{{2, 1}, {3, 2}, {5, 5}, {6, 6}, {7, 7}},
					},
					{
						lset:    labels.FromStrings("a", "b"),
						samples: []sample{{1, 1}, {2, 2}, {3, 3}, {4, 4}},
					},
					{
						lset:    labels.FromStrings("a", "c"),
						samples: []sample{{100, 1}, {300, 3}},
					},
				},
				expectedAfterDedup: series{
					lset: labels.Labels{},
					// We don't expect correctness here, it's just random non-replica data.
					samples: []sample{{1, 1}, {2, 2}, {3, 3}, {4, 4}},
				},
				expectedWarning: "partial error",
			},

		g := gate.New(2)
		q := newQuerier(context.Background(), logger, realSeriesWithStaleMarkerMint, realSeriesWithStaleMarkerMaxt, []string{"replica"}, nil, s, false, 0, true, false, g, timeout)
		t.Cleanup(func() {
			testutil.Ok(t, q.Close())
		})

		e := promql.NewEngine(promql.EngineOpts{
			Logger:     logger,
			Timeout:    timeout,
			MaxSamples: math.MaxInt64,
		})
		t.Run("range", func(t *testing.T) {
			q, err := e.NewRangeQuery(&mockedQueryable{querier: q}, `rate(gitlab_transaction_cache_read_hit_count_total[5m])`, timestamp.Time(realSeriesWithStaleMarkerMint).Add(5*time.Minute), timestamp.Time(realSeriesWithStaleMarkerMaxt), 100*time.Second)
			testutil.Ok(t, err)

			r := q.Exec(context.Background())
			testutil.Ok(t, r.Err)
			testutil.Assert(t, len(r.Warnings) == 0)

			vec, err := r.Matrix()
			testutil.Ok(t, err)
			testutil.Equals(t, promql.Matrix{
				{Metric: labels.FromStrings(), Points: []promql.Point{
					{T: 1587690300000, V: 13.652631578947368}, {T: 1587690400000, V: 14.049122807017545}, {T: 1587690500000, V: 13.961403508771928}, {T: 1587690600000, V: 13.617543859649123}, {T: 1587690700000, V: 14.568421052631578}, {T: 1587690800000, V: 14.989473684210525},
					{T: 1587690900000, V: 16.2}, {T: 1587691000000, V: 16.052631578947366}, {T: 1587691100000, V: 15.831578947368419}, {T: 1587691200000, V: 15.659649122807016}, {T: 1587691300000, V: 14.842105263157894}, {T: 1587691400000, V: 14.003508771929825},
					{T: 1587691500000, V: 13.782456140350877}, {T: 1587691600000, V: 13.863157894736842}, {T: 1587691700000, V: 15.270282598474374}, {T: 1587691800000, V: 14.343859649122805}, {T: 1587691900000, V: 13.975438596491228}, {T: 1587692000000, V: 13.4},
					{T: 1587692100000, V: 14.087719298245615}, {T: 1587692200000, V: 14.39298245614035}, {T: 1587692300000, V: 15.024561403508772}, {T: 1587692400000, V: 14.073684210526313}, {T: 1587692500000, V: 9.3772165751634}, {T: 1587692600000, V: 6.378947368421052},
					{T: 1587692700000, V: 8.19298245614035}, {T: 1587692800000, V: 11.918703026416258}, {T: 1587692900000, V: 13.75813610765101}, {T: 1587693000000, V: 13.087719298245615}, {T: 1587693100000, V: 13.466666666666667}, {T: 1587693200000, V: 14.028070175438595},
					{T: 1587693300000, V: 14.23859649122807}, {T: 1587693400000, V: 15.407017543859647}, {T: 1587693500000, V: 15.915789473684208}, {T: 1587693600000, V: 15.712280701754386},
				}},
				{Metric: labels.FromStrings(), Points: []promql.Point{
					{T: 1587690300000, V: 13.69122807017544}, {T: 1587690400000, V: 14.098245614035086}, {T: 1587690500000, V: 13.905263157894735}, {T: 1587690600000, V: 13.617543859649123}, {T: 1587690700000, V: 14.350877192982455}, {T: 1587690800000, V: 15.003508771929825},
					{T: 1587690900000, V: 16.12280701754386}, {T: 1587691000000, V: 16.049122807017543}, {T: 1587691100000, V: 15.922807017543859}, {T: 1587691200000, V: 15.63157894736842}, {T: 1587691300000, V: 14.982456140350878}, {T: 1587691400000, V: 14.187259188557551},
					{T: 1587691500000, V: 13.828070175438594}, {T: 1587691600000, V: 13.971929824561403}, {T: 1587691700000, V: 15.31994329585807}, {T: 1587691800000, V: 14.30877192982456}, {T: 1587691900000, V: 13.915789473684212}, {T: 1587692000000, V: 13.312280701754384},
					{T: 1587692100000, V: 14.136842105263158}, {T: 1587692200000, V: 14.39298245614035}, {T: 1587692300000, V: 15.014035087719297}, {T: 1587692400000, V: 14.112280701754386}, {T: 1587692500000, V: 9.421065148148147}, {T: 1587692600000, V: 6.421368067203301},
					{T: 1587692700000, V: 8.252631578947367}, {T: 1587692800000, V: 11.721237543747266}, {T: 1587692900000, V: 13.842105263157894}, {T: 1587693000000, V: 13.153509064307995}, {T: 1587693100000, V: 13.378947368421052}, {T: 1587693200000, V: 14.03157894736842},
					{T: 1587693300000, V: 14.147368421052631}, {T: 1587693400000, V: 15.343159785693985}, {T: 1587693500000, V: 15.90877192982456}, {T: 1587693600000, V: 15.761403508771927},
				}},
			}, vec)
		})
	})
}
*/
