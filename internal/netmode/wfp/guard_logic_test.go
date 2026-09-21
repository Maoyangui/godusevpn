//go:build windows

package wfp

import "testing"

func TestBootGuardCoversRequiresEnabledBlockingLayer(t *testing.T) {
	layers := ourLayers()
	fs := make([]filterInfo, 0, len(layers))
	for _, layer := range layers {
		fs = append(fs, filterInfo{layer: layer, action: cFWP_ACTION_BLOCK})
	}
	if bootGuardCovers(fs) {
		t.Fatal("non-boot filters must not satisfy boot guard")
	}
	for i := range fs {
		fs[i].flags = cFWPM_FILTER_FLAG_BOOTTIME
	}
	if !bootGuardCovers(fs) {
		t.Fatal("all enabled blocking boot layers should satisfy boot guard")
	}
	fs[0].action = cFWP_ACTION_PERMIT
	if bootGuardCovers(fs) {
		t.Fatal("a permit filter must not satisfy boot guard")
	}
}

func TestGuardCoversRequiresEveryLayerAndFlag(t *testing.T) {
	layers := ourLayers()
	fs := make([]filterInfo, len(layers))
	for i, layer := range layers {
		fs[i] = filterInfo{layer: layer, action: cFWP_ACTION_BLOCK, flags: cFWPM_FILTER_FLAG_PERSISTENT}
	}
	if !guardCovers(fs, cFWPM_FILTER_FLAG_PERSISTENT) {
		t.Fatal("complete persistent blocking set should pass")
	}
	fs[0].flags = cFWPM_FILTER_FLAG_BOOTTIME
	if guardCovers(fs, cFWPM_FILTER_FLAG_PERSISTENT) {
		t.Fatal("missing persistent layer must fail")
	}
	fs[0].flags = cFWPM_FILTER_FLAG_PERSISTENT | cFWPM_FILTER_FLAG_DISABLED
	if guardCovers(fs, cFWPM_FILTER_FLAG_PERSISTENT) {
		t.Fatal("disabled blocking layer must fail")
	}
}
