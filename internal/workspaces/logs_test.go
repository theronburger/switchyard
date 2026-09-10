package workspaces

import (
	"strings"
	"testing"
)

func TestRunLogsBoundOutputAndRedactCredentialsAndScripts(t *testing.T) {
	log := &runLog{}
	log.remember("echo configured-command\nrun --private-flag", []string{"APP_SECRET=environment-sentinel"})
	for _, fragment := range []string{"Authorization: Bear", "er bearer-sentinel\nAPI_TOKEN='quoted sentinel'\n", "environment-sentinel person@example.invalid https://login:password@example.invalid/path\n", "run --private-flag\nordinary startup error\n"} {
		_, _ = log.Write([]byte(fragment))
	}
	text := log.tail(4096)
	for _, forbidden := range []string{"sentinel", "person@", "login", "--private-flag"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("log exposed %s: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "ordinary startup error") {
		t.Fatalf("useful failure disappeared: %s", text)
	}
	_, _ = log.Write([]byte("TOKEN=" + strings.Repeat("private", maximumLogBytes)))
	_, _ = log.Write([]byte("\nlast safe line\n"))
	if len(log.contents) > maximumLogBytes || strings.Contains(log.tail(4096), "private") || !strings.Contains(log.tail(4096), "last safe line") {
		t.Fatal("bounded log exposed a truncated credential line")
	}
}
