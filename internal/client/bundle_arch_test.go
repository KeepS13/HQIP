package client

import (
	"context"
	"testing"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

// A valid signature does not make a package executable on another ABI. The
// updater must ignore it before downloading and accept the matching release.
func TestBundleUpdateKeepsAndroidArchitecturesSeparate(t *testing.T) {
	for _, platform := range []string{"android-arm64", "android-amd64"} {
		t.Run(platform, func(t *testing.T) {
			f := newBundleFixture(t)
			u := f.updater()
			u.platform = platform
			r, err := f.catalog.Releases[0].Decode()
			if err != nil {
				t.Fatal(err)
			}
			r.Platform = "android-arm64"
			if platform == r.Platform {
				r.Platform = "android-amd64"
			}
			r.Filename = clientrelease.Filename(r.Platform)
			f.catalog.Releases[0], err = clientrelease.Sign(r, f.private)
			if err != nil {
				t.Fatal(err)
			}
			if err = u.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if f.downloads != 0 || u.View().Ready {
				t.Fatal("downloaded a foreign ABI")
			}
			r.Platform, r.Filename = platform, clientrelease.Filename(platform)
			f.catalog.Releases[0], err = clientrelease.Sign(r, f.private)
			if err != nil {
				t.Fatal(err)
			}
			if err = u.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if f.downloads != 1 || !u.View().Ready {
				t.Fatal("matching ABI was not staged")
			}
			if u.View().Message != "新版已下载，点击安装并在系统提示中确认" {
				t.Fatal("both Android ABIs require system installation consent")
			}
		})
	}
}
