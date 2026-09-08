package workspaces

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
)

const maximumLogBytes = 256 * 1024

type runLog struct {
	mu        sync.Mutex
	contents  []byte
	scripts   []string
	secrets   []string
	truncated bool
}

func (log *runLog) Write(contents []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	count := len(contents)
	if count >= maximumLogBytes {
		log.truncated = true
		log.contents = append(log.contents[:0], contents[count-maximumLogBytes:]...)
		return count, nil
	}
	if excess := len(log.contents) + count - maximumLogBytes; excess > 0 {
		log.truncated = true
		log.contents = append(log.contents[:0], log.contents[excess:]...)
	}
	log.contents = append(log.contents, contents...)
	return count, nil
}

func (log *runLog) remember(script string, environment []string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.scripts = append(log.scripts, script)
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		if sensitiveName.MatchString(name) && value != "" {
			log.secrets = append(log.secrets, value)
		}
	}
}

var sensitiveName = regexp.MustCompile(`(?i)(token|secret|password|passwd|api.?key|credential|authorization)`)
var credentialAssignment = regexp.MustCompile(`(?i)((?:[a-z0-9_]*(?:token|secret|password|passwd|api.?key|credential)[a-z0-9_]*|authorization)\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
var bearerCredential = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9_+/=.-]+`)
var emailAddress = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
var terminalEscape = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
var urlCredentials = regexp.MustCompile(`(https?://)[^/@\s]+@`)

func (log *runLog) tail(maxBytes int) string {
	log.mu.Lock()
	defer log.mu.Unlock()
	text := terminalEscape.ReplaceAllString(string(log.contents), "")
	if log.truncated {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = text[newline+1:]
		} else {
			text = ""
		}
	}
	for _, script := range log.scripts {
		for _, line := range strings.Split(script, "\n") {
			if strings.TrimSpace(line) != "" {
				text = strings.ReplaceAll(text, line, "[command]")
			}
		}
	}
	for _, secret := range log.secrets {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	text = bearerCredential.ReplaceAllString(text, "$1 [redacted]")
	text = credentialAssignment.ReplaceAllString(text, "$1[redacted]")
	text = urlCredentials.ReplaceAllString(text, "$1[redacted]@")
	text = emailAddress.ReplaceAllString(text, "[email]")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		text = strings.ReplaceAll(text, home, "~")
	}
	if maxBytes <= 0 || maxBytes > maximumLogBytes {
		maxBytes = maximumLogBytes
	}
	if len(text) > maxBytes {
		text = text[len(text)-maxBytes:]
	}
	return text
}

func (engine *Engine) Logs(path string, maxBytes int) (LogOutput, error) {
	path, err := physicalWorkspacePath(path)
	if err != nil {
		return LogOutput{}, err
	}
	engine.mu.Lock()
	run := engine.runs[path]
	engine.mu.Unlock()
	if run == nil {
		return LogOutput{}, errors.New("this workspace has no run logs yet")
	}
	return LogOutput{RunID: run.result.ID, Text: run.logs.tail(maxBytes)}, nil
}
