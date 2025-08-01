package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/moby/moby/v2/daemon/server/httputils"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestNewVersionMiddlewareValidation(t *testing.T) {
	tests := []struct {
		doc, defaultVersion, minVersion, expectedErr string
	}{
		{
			doc:            "defaults",
			defaultVersion: "1.52",
			minVersion:     "1.24",
		},
		{
			doc:            "invalid default lower than min",
			defaultVersion: "1.24",
			minVersion:     "1.52",
			expectedErr:    "invalid API version: the minimum API version (1.52) is higher than the default version (1.24)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.doc, func(t *testing.T) {
			_, err := NewVersionMiddleware("1.2.3", tc.defaultVersion, tc.minVersion)
			if tc.expectedErr == "" {
				assert.Check(t, err)
			} else {
				assert.Check(t, is.Error(err, tc.expectedErr))
			}
		})
	}
}

func TestVersionMiddlewareVersion(t *testing.T) {
	expectedVersion := "<not set>"
	handler := func(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
		v := httputils.VersionFromContext(ctx)
		assert.Check(t, is.Equal(expectedVersion, v))
		return nil
	}

	m, err := NewVersionMiddleware("1.2.3", "1.52", "1.24")
	assert.NilError(t, err)
	h := m.WrapHandler(handler)

	req, _ := http.NewRequest(http.MethodGet, "/containers/json", http.NoBody)
	resp := httptest.NewRecorder()
	ctx := context.Background()

	tests := []struct {
		reqVersion      string
		expectedVersion string
		errString       string
	}{
		{
			expectedVersion: "1.52",
		},
		{
			reqVersion:      "1.24",
			expectedVersion: "1.24",
		},
		{
			reqVersion: "0.1",
			errString:  "client version 0.1 is too old. Minimum supported API version is 1.24, please upgrade your client to a newer version",
		},
		{
			reqVersion: "1.23",
			errString:  "client version 1.23 is too old. Minimum supported API version is 1.24, please upgrade your client to a newer version",
		},
		{
			reqVersion: "9999.9999",
			errString:  "client version 9999.9999 is too new. Maximum supported API version is 1.52",
		},
	}

	for _, test := range tests {
		expectedVersion = test.expectedVersion

		err := h(ctx, resp, req, map[string]string{"version": test.reqVersion})

		if test.errString != "" {
			assert.Check(t, is.Error(err, test.errString))
		} else {
			assert.Check(t, err)
		}
	}
}

func TestVersionMiddlewareWithErrorsReturnsHeaders(t *testing.T) {
	handler := func(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
		v := httputils.VersionFromContext(ctx)
		assert.Check(t, v != "")
		return nil
	}

	m, err := NewVersionMiddleware("1.2.3", "1.52", "1.24")
	assert.NilError(t, err)
	h := m.WrapHandler(handler)

	req, _ := http.NewRequest(http.MethodGet, "/containers/json", http.NoBody)
	resp := httptest.NewRecorder()
	ctx := context.Background()

	vars := map[string]string{"version": "0.1"}
	err = h(ctx, resp, req, vars)
	assert.Check(t, is.ErrorContains(err, ""))

	hdr := resp.Result().Header
	assert.Check(t, is.Contains(hdr.Get("Server"), "Docker/1.2.3"))
	assert.Check(t, is.Contains(hdr.Get("Server"), runtime.GOOS))
	assert.Check(t, is.Equal(hdr.Get("Api-Version"), "1.52"))
	assert.Check(t, is.Equal(hdr.Get("Ostype"), runtime.GOOS))
}
