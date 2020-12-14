// Copyright (c) The Thanos Authors.
// Licensed under the Apache License 2.0.

package query

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/thanos-io/thanos/pkg/component"
	"github.com/thanos-io/thanos/pkg/store"
	"github.com/thanos-io/thanos/pkg/testutil"
)

func TestMain(m *testing.M) {
	testutil.TolerantVerifyLeakMain(m)
}

func TestQuerier_Proxy(t *testing.T) {
	files, err := filepath.Glob("testdata/*.test")
	testutil.Ok(t, err)
	testutil.Equals(t, 9, len(files))

	logger := log.NewNopLogger()
	for _, fn := range files {
		t.Run(fn, func(t *testing.T) {
			t.Run("proxy", func(t *testing.T) {
				storesFn := func() []store.Client { return nil }
				q := NewQueryableCreator(
					logger,
					nil,
					store.NewProxyStore(logger, nil, storesFn, component.Debug, nil, 5*time.Minute),
					1000000,
					5*time.Minute,
				)

				te, err := newTestFromFile(t, fn)
				testutil.Ok(t, err)
				testutil.Ok(t, te.run())
				te.close()
			})

		})
	}
}
