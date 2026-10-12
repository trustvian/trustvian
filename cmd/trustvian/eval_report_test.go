package main

// trustvian eval report, task 107: one GET, the server's body printed
// unchanged in both output modes, and the eval family's exit codes — a FAIL
// verdict in the report is not a failed command.

import (
	"strings"
	"testing"
)

const reportBody = `{"version":"2","scenario":{"name":"support","runs":3},"execution_id":"exec/1",` +
	`"comparison":{"gate":{"verdict":"fail","state":"recorded"}},"extra":"kept"}`

func TestEvalReportPrintsTheDocumentUnchanged(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "report", "exec/1"},
		{"eval", "report", "exec/1", "--json"},
		{"eval", "report", "--json", "exec/1"},
	} {
		api := newFakeAPI(t)
		api.reply(200, reportBody)
		result := runPlatformCLI(t, append(args, "--api-url", api.url())...)
		result.mustExit(t, exitOK, strings.Join(args, " "))
		request := api.only()
		if request.method != "GET" || request.escapedPath != "/v1/scenario-executions/exec%2F1/report" {
			t.Fatalf("request = %s %s", request.method, request.escapedPath)
		}
		if result.stdout != reportBody+"\n" {
			t.Fatalf("stdout %q is not the server's body", result.stdout)
		}
	}
}

func TestEvalReportExitCodes(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{"no id", []string{"eval", "report"}, exitUsage},
		{"two ids", []string{"eval", "report", "a", "b"}, exitUsage},
		{"unknown flag", []string{"eval", "report", "a", "--format", "md"}, exitUsage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := runPlatformCLI(t, append(tt.args, "--api-url", "http://127.0.0.1:1")...)
			result.mustExit(t, tt.want, tt.name)
		})
	}
	for status, body := range map[int]string{
		404: `{"error":{"code":"not_found","message":"no such execution"}}`,
		409: `{"error":{"code":"conflict","message":"execution is running"}}`,
	} {
		api := newFakeAPI(t)
		api.reply(status, body)
		result := runPlatformCLI(t, "eval", "report", "e1", "--api-url", api.url())
		result.mustExit(t, exitOperational, "API refusal")
		if result.stdout != "" {
			t.Fatalf("a refusal printed %q", result.stdout)
		}
	}
}
