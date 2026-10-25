package rootless

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestMappingsPreserveHostIDs(t *testing.T) {
	sub := []specs.LinuxIDMapping{{HostID: 100000, ContainerID: 50, Size: 65536}, {HostID: 300000, Size: 1000}}
	got := mappings(1000, sub)
	want := []specs.LinuxIDMapping{{HostID: 1000, Size: 1}, {HostID: 100000, ContainerID: 1, Size: 65536}, {HostID: 300000, ContainerID: 65537, Size: 1000}}
	if !reflect.DeepEqual(got, want) || sub[0].ContainerID != 50 {
		t.Fatalf("mappings: %+v; input: %+v", got, sub)
	}
}

func TestCanceledRunDoesNotStartChild(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started, err := Run(ctx, []string{"regview"}, nil, io.Discard, io.Discard)
	if started || !errors.Is(err, context.Canceled) {
		t.Fatalf("started=%v err=%v", started, err)
	}
}
