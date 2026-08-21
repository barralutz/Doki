package runtime

import (
	"reflect"
	"testing"
)

func TestBuildCommandUsesImageEntrypointAndCmdWhenRequestDoesNotOverride(t *testing.T) {
	img := &ImageOCIConfig{
		Entrypoint: []string{"docker-entrypoint.sh"},
		Cmd:        []string{"postgres"},
	}

	got := BuildCommand(nil, nil, img)
	want := []string{"docker-entrypoint.sh", "postgres"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildCommand() = %#v, want %#v", got, want)
	}
}
