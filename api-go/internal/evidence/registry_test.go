package evidence

import "testing"

func TestRunWrapperCommandHintsMatchRegistryFamily(t *testing.T) {
	for _, tc := range []struct {
		line string
		hint string
	}{
		{"node --test tests/*.js", "node --test"},
		{"docker compose logs -f api", "docker logs"},
		{"podman logs api", "podman logs"},
		{"oc logs pod", "oc logs"},
		{"kubectl -n ns logs pod", "kubectl logs"},
		{"g++ main.cpp", "g++"},
		{"clang++ main.cpp", "clang++"},
		{"npm run test:unit", "npm run test"},
		{"make -j4 check-backend", "make check"},
	} {
		lineFamily, _ := commandFamily(tc.line)
		hintFamily, _ := commandFamily(tc.hint)
		if lineFamily != hintFamily {
			t.Errorf("commandFamily(%q) = %q, commandFamily(%q) = %q", tc.line, lineFamily, tc.hint, hintFamily)
		}
	}
}
