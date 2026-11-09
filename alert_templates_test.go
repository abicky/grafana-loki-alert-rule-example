package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"text/template"
)

type fallbackRule struct {
	Label    string `json:"label"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
	URL      string `json:"url"`
}

type alertTemplateConfig struct {
	SlackMessage  string            `json:"slack_message"`
	Annotation    string            `json:"annotation"`
	LatestQueries map[string]string `json:"latest_queries"`
	CountQueries  map[string]string `json:"count_queries"`
	Grouping      string            `json:"grouping"`
}

// Evaluate the real locals in an isolated module without providers, state, or credentials.
func terraformAlertTemplateConfig(t *testing.T, rules []fallbackRule) alertTemplateConfig {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"locals.tf", "variables.tf"} {
		content, err := os.ReadFile(filepath.Join("terraform", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}

	args := []string{"console", "-no-color", "-var=slack_webhook_url=unused"}
	if rules != nil {
		content, err := json.Marshal(map[string]any{"runbook_fallback_rules": rules})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rules.tfvars.json"), content, 0600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-var-file=rules.tfvars.json")
	}

	cmd := exec.CommandContext(t.Context(), "terraform", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(`jsonencode({slack_message = local.slack_message, annotation = local.rules[0].annotations.runbook_url, latest_queries = local.latest_error_queries, count_queries = local.count_queries, grouping = local.grouping_labels})` + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("terraform console: %v\n%s", err, stderr.String())
	}

	decoded, err := strconv.Unquote(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatal(err)
	}

	var config alertTemplateConfig
	if err := json.Unmarshal([]byte(decoded), &config); err != nil {
		t.Fatal(err)
	}

	return config
}

type runbookTestCase struct {
	name   string
	config alertTemplateConfig

	// A nil map means that the latest_error query value is absent.
	latestLabels map[string]string
	alertLabels  map[string]string
	want         string
}

func testRunbookAnnotation(t *testing.T, tests []runbookTestCase) {
	t.Helper()

	type queryValue struct{ Labels map[string]string }
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Grafana uses Go templates and regexp.MatchString for its match function.
			// Grafana v13.3.0 delegates to Prometheus's template engine:
			// See: https://github.com/grafana/grafana/blob/v13.3.0/pkg/services/ngalert/state/template/template.go#L146-L147
			// Its Prometheus v0.313.0 dependency registers match as regexp.MatchString:
			// See: https://github.com/prometheus/prometheus/blob/v0.313.0/template/template.go#L171
			tmpl, err := template.New("runbook").Funcs(template.FuncMap{
				"match": regexp.MatchString,
			}).Parse("{{ $values := .Values }}{{ $labels := .Labels }}" + tt.config.Annotation)
			if err != nil {
				t.Fatalf("parse runbook annotation: %v", err)
			}

			values := make(map[string]queryValue)
			if tt.latestLabels != nil {
				values["latest_error"] = queryValue{Labels: tt.latestLabels}
			}
			// Mirror Grafana's $values and $labels template variables. Alert labels
			// are included so tests can verify that only latest-error labels are used.
			// See: https://grafana.com/docs/grafana/latest/alerting/alerting-rules/templates/reference/#variables
			data := struct {
				Values map[string]queryValue
				Labels map[string]string
			}{
				Values: values,
				Labels: tt.alertLabels,
			}

			var rendered bytes.Buffer
			if err := tmpl.Execute(&rendered, data); err != nil {
				t.Fatalf("render runbook annotation: %v", err)
			}
			if got := rendered.String(); got != tt.want {
				t.Errorf("rendered URL = %q; want %q", got, tt.want)
			}
		})
	}
}

func testRunbookQueryLabels(t *testing.T, config, baseline alertTemplateConfig, rules []fallbackRule) {
	t.Helper()

	if config.Grouping != baseline.Grouping {
		t.Errorf("grouping = %q; want unchanged grouping %q", config.Grouping, baseline.Grouping)
	}
	if !maps.Equal(config.CountQueries, baseline.CountQueries) {
		t.Errorf("count queries = %q; want unchanged queries %q", config.CountQueries, baseline.CountQueries)
	}

	maxBy := regexp.MustCompile(`max by \(([^)]+)\)`)
	for severity, query := range config.LatestQueries {
		t.Run(severity, func(t *testing.T) {
			if want := "topk by (" + config.Grouping + ")"; !strings.HasPrefix(query, want) {
				t.Errorf("latest query prefix = %q; want %q", query, want)
			}
			countQuery := config.CountQueries[severity]
			if want := "sum by (" + config.Grouping + ")"; !strings.HasPrefix(countQuery, want) {
				t.Errorf("count query prefix = %q; want %q", countQuery, want)
			}

			match := maxBy.FindStringSubmatch(query)
			if len(match) != 2 {
				t.Fatalf("latest query = %q; want max-by aggregation", query)
			}
			preserved := strings.Split(match[1], ", ")
			for _, rule := range rules {
				if !slices.Contains(preserved, rule.Label) {
					t.Errorf("latest query labels = %q; want to preserve %q", preserved, rule.Label)
				}
			}
		})
	}
}

func TestRunbookFallbacks(t *testing.T) {
	rules := []fallbackRule{
		{
			Label:    "code",
			Operator: "eq",
			Value:    "DB",
			URL:      "https://example.com/code-eq-DB",
		},
		{
			Label:    "owner",
			Operator: "ne",
			Value:    "ignored",
			URL:      "https://example.com/owner-ne-ignored",
		},
		{
			Label:    "exception_message",
			Operator: "match",
			Value:    `configuration .* unavailable`,
			URL:      "https://example.com/configuration-message",
		},
		{
			Label:    "operation",
			Operator: "match",
			Value:    `^query-[0-9]+$`,
			URL:      "https://example.com/query-operation",
		},
		{
			Label:    "code",
			Operator: "eq",
			Value:    "DB",
			URL:      "https://example.com/code-eq-DB-later",
		},
		{
			Label:    "empty",
			Operator: "eq",
			Value:    "",
			URL:      "https://example.com/empty-eq-empty-string",
		},
		{
			Label:    "exception_type",
			Operator: "eq",
			Value:    "main.databaseError",
			URL:      "https://example.com/exception_type-eq-main.databaseError",
		},
	}
	operatorsConfig := terraformAlertTemplateConfig(t, rules)
	noRulesConfig := terraformAlertTemplateConfig(t, []fallbackRule{})
	firstEmptyURLConfig := terraformAlertTemplateConfig(t, []fallbackRule{
		{Label: "code", Operator: "eq", Value: "DB", URL: ""},
		{Label: "code", Operator: "eq", Value: "DB", URL: "later"},
	})

	tests := []runbookTestCase{
		{
			name:         "equality",
			config:       operatorsConfig,
			latestLabels: map[string]string{"code": "DB"},
			want:         "https://example.com/code-eq-DB",
		},
		{
			name:         "existing grouping label",
			config:       operatorsConfig,
			latestLabels: map[string]string{"exception_type": "main.databaseError"},
			want:         "https://example.com/exception_type-eq-main.databaseError",
		},
		{
			name:         "inequality",
			config:       operatorsConfig,
			latestLabels: map[string]string{"owner": "payments"},
			want:         "https://example.com/owner-ne-ignored",
		},
		{
			name:         "inequality excludes equal",
			config:       operatorsConfig,
			latestLabels: map[string]string{"owner": "ignored"},
			want:         "",
		},
		{
			name:         "match finds an unanchored pattern",
			config:       operatorsConfig,
			latestLabels: map[string]string{"exception_message": "prefix configuration is unavailable: missing required key"},
			want:         "https://example.com/configuration-message",
		},
		{
			name:         "match rejects nonmatching message",
			config:       operatorsConfig,
			latestLabels: map[string]string{"exception_message": "foofoo"},
			want:         "",
		},
		{
			name:         "anchored match",
			config:       operatorsConfig,
			latestLabels: map[string]string{"operation": "query-123"},
			want:         "https://example.com/query-operation",
		},
		{
			name:         "anchored match rejects partial input",
			config:       operatorsConfig,
			latestLabels: map[string]string{"operation": "prefix-query-123"},
			want:         "",
		},
		{
			name:         "missing labels including inequality",
			config:       operatorsConfig,
			latestLabels: map[string]string{},
			want:         "",
		},
		{
			name:         "no match",
			config:       operatorsConfig,
			latestLabels: map[string]string{"code": "OTHER", "owner": "ignored"},
			want:         "",
		},
		{
			name:         "first overlapping rule wins",
			config:       operatorsConfig,
			latestLabels: map[string]string{"code": "DB", "owner": "payments", "operation": "query-1"},
			want:         "https://example.com/code-eq-DB",
		},
		{
			name:         "explicit URL wins",
			config:       operatorsConfig,
			latestLabels: map[string]string{"code": "DB", "runbook_url": "https://example.com/explicit"},
			want:         "https://example.com/explicit",
		},
		{
			name:         "empty explicit URL uses fallback",
			config:       operatorsConfig,
			latestLabels: map[string]string{"code": "DB", "runbook_url": ""},
			want:         "https://example.com/code-eq-DB",
		},
		{
			name:         "present empty label",
			config:       operatorsConfig,
			latestLabels: map[string]string{"empty": ""},
			want:         "https://example.com/empty-eq-empty-string",
		},
		{
			name:         "present empty inequality",
			config:       operatorsConfig,
			latestLabels: map[string]string{"owner": ""},
			want:         "https://example.com/owner-ne-ignored",
		},
		{
			name:        "no latest error ignores alert labels",
			config:      operatorsConfig,
			alertLabels: map[string]string{"code": "DB"},
			want:        "",
		},
		{
			name:         "uses latest error instead of alert labels",
			config:       operatorsConfig,
			latestLabels: map[string]string{"code": "OTHER"},
			alertLabels:  map[string]string{"code": "DB"},
			want:         "",
		},
		{
			name:         "no fallback",
			config:       noRulesConfig,
			latestLabels: map[string]string{"exception_type": "main.databaseError"},
			want:         "",
		},
		{
			name:         "explicit URL",
			config:       noRulesConfig,
			latestLabels: map[string]string{"runbook_url": "explicit"},
			want:         "explicit",
		},
		{
			name:         "first matching rule wins",
			config:       firstEmptyURLConfig,
			latestLabels: map[string]string{"code": "DB"},
			want:         "",
		},
	}

	t.Run("annotation", func(t *testing.T) {
		testRunbookAnnotation(t, tests)
	})
	t.Run("query labels", func(t *testing.T) {
		testRunbookQueryLabels(t, operatorsConfig, noRulesConfig, rules)
	})
}

func TestDemoRunbookFallbacks(t *testing.T) {
	config := terraformAlertTemplateConfig(t, nil)
	t.Run("annotation", func(t *testing.T) {
		var configurationLabels map[string]string
		for _, ev := range cycleEvents() {
			if ev.Scenario == "errors-new" {
				configurationLabels = map[string]string{
					"exception_type":    exceptionType(ev.Err),
					"exception_message": ev.Err.Error(),
				}
				break
			}
		}
		if configurationLabels == nil {
			t.Fatal("configuration demo event is missing")
		}

		testRunbookAnnotation(t, []runbookTestCase{
			{
				name:   "database exception",
				config: config,
				latestLabels: map[string]string{
					"exception_type": exceptionType(databaseError{"timeout"}),
				},
				want: "https://example.com/runbooks/database",
			},
			{
				name:         "configuration message emitted by demo",
				config:       config,
				latestLabels: configurationLabels,
				want:         "https://example.com/runbooks/configuration",
			},
		})
	})
	t.Run("query labels", func(t *testing.T) {
		for severity, query := range config.LatestQueries {
			if !strings.Contains(query, "exception_message") {
				t.Errorf("%s latest query = %q; want to preserve exception_message", severity, query)
			}
		}
	})
}

// notificationDataFuncs supplies the data.ToJSON namespace used by the Slack template.
// GetTemplate passes kind-specific options to fromContent; for GrafanaKind,
// these include addFuncs, which installs gomplate.FuncMap on the templates.
// See:
// - https://github.com/grafana/alerting/blob/71374176ca7b/templates/factory.go#L60-L70
// - https://github.com/grafana/alerting/blob/71374176ca7b/templates/funcs.go#L26-L32
// - https://github.com/grafana/alerting/blob/71374176ca7b/templates/template_data.go#L160-L176
//
// That function map registers the data namespace, whose ToJSON uses encoding/json.Marshal.
// See:
// - https://github.com/grafana/alerting/blob/71374176ca7b/templates/gomplate/funcs.go#L12-L27
// - https://github.com/grafana/alerting/blob/71374176ca7b/templates/gomplate/data.go#L39-L53
// - https://github.com/grafana/alerting/blob/71374176ca7b/templates/gomplate/data.go#L70-L76
type notificationDataFuncs struct{}

func (notificationDataFuncs) ToJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}

	return string(encoded), nil
}

// slackAlert contains the notification fields read by local.slack_message.
// See: https://grafana.com/docs/grafana/latest/alerting/configure-notifications/template-notifications/reference/#alert
type slackAlert struct {
	Labels       map[string]string
	Annotations  map[string]string
	GeneratorURL string
	DashboardURL string
	SilenceURL   string
}

func renderSlackMessage(t *testing.T, message string, alerts []slackAlert) string {
	t.Helper()

	tmpl, err := template.New("slack_message").Funcs(template.FuncMap{
		"data": func() notificationDataFuncs { return notificationDataFuncs{} },
	}).Parse(message)
	if err != nil {
		t.Fatalf("parse Slack message: %v", err)
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, struct{ Alerts []slackAlert }{Alerts: alerts}); err != nil {
		t.Fatalf("render Slack message: %v", err)
	}

	return rendered.String()
}

func TestSlackMessage(t *testing.T) {
	config := terraformAlertTemplateConfig(t, nil)
	const silenceURLPrefix = "https://example.com/"
	maximumSilenceURL := silenceURLPrefix + strings.Repeat("s", 4000-len(silenceURLPrefix))
	longStack := strings.Repeat("\u3042", 3001)
	tests := []struct {
		name   string
		alerts []slackAlert
		want   string
	}{
		{
			name: "description without optional sections",
			alerts: []slackAlert{{
				Annotations: map[string]string{"description": "Database request failed"},
			}},
			want: `Database request failed
`,
		},
		{
			name: "all action links",
			alerts: []slackAlert{{
				Annotations: map[string]string{
					"runbook_url": "https://example.com/runbook",
				},
				GeneratorURL: "https://example.com/rule",
				DashboardURL: "https://example.com/dashboard",
				SilenceURL:   "https://example.com/silence",
			}},
			want: `<no value>
*Actions*
- <https://example.com/runbook|Go to runbook>
- <https://example.com/rule|Go to alert rule>
- <https://example.com/dashboard|Go to dashboard>
- <https://example.com/silence|Create silence>
`,
		},
		{
			name: "missing action links leave no empty lines",
			alerts: []slackAlert{{
				Annotations:  map[string]string{"stacktrace": "database.go:20"},
				GeneratorURL: "https://example.com/rule",
				SilenceURL:   "https://example.com/silence",
			}},
			want: `<no value>
*Actions*
- <https://example.com/rule|Go to alert rule>
- <https://example.com/silence|Create silence>
*Stack trace*
` + "```" + `
database.go:20
` + "```" + `
`,
		},
		{
			name: "runbook is the only action",
			alerts: []slackAlert{{
				Annotations: map[string]string{"runbook_url": "https://example.com/runbook"},
			}},
			want: `<no value>
*Actions*
- <https://example.com/runbook|Go to runbook>
`,
		},
		{
			name: "silence URL at limit",
			alerts: []slackAlert{{
				SilenceURL: maximumSilenceURL,
			}},
			want: `<no value>
*Actions*
- <` + maximumSilenceURL + `|Create silence>
`,
		},
		{
			name: "oversized silence URL omitted",
			alerts: []slackAlert{{
				SilenceURL: maximumSilenceURL + "s",
			}},
			want: `<no value>
`,
		},
		{
			name: "oversized silence URL does not hide other actions",
			alerts: []slackAlert{{
				GeneratorURL: "https://example.com/rule",
				SilenceURL:   maximumSilenceURL + "s",
			}},
			want: `<no value>
*Actions*
- <https://example.com/rule|Go to alert rule>
`,
		},
		{
			name: "stack trace",
			alerts: []slackAlert{{
				Annotations: map[string]string{"stacktrace": "database.go:20\nmain.go:10"},
			}},
			want: `<no value>
*Stack trace*
` + "```" + `
database.go:20
main.go:10
` + "```" + `
`,
		},
		{
			name: "stack trace truncated by characters",
			alerts: []slackAlert{{
				Annotations: map[string]string{"stacktrace": longStack},
			}},
			want: `<no value>
*Stack trace*
` + "```" + `
` + strings.Repeat("\u3042", 3000) + `
` + "```" + `
`,
		},
		{
			name: "internal fields omitted",
			alerts: []slackAlert{{
				Labels: map[string]string{"service_name": "hidden-service", "error_group": "hidden-fingerprint"},
				Annotations: map[string]string{
					"summary":       "hidden-summary",
					"description":   "Visible description",
					"explore_query": "hidden-query",
					"explore_from":  "123",
				},
			}},
			want: `Visible description
`,
		},
		{
			name: "additional labels only",
			alerts: []slackAlert{{
				Labels: map[string]string{"severity": "ERROR", "team": "payments"},
			}},
			want: `<no value>
*Other labels:*
- severity: ERROR
- team: payments
`,
		},
		{
			name: "additional annotations only",
			alerts: []slackAlert{{
				Annotations: map[string]string{"owner": "on-call", "ticket": "OPS-1"},
			}},
			want: `<no value>
*Other annotations:*
- owner: on-call
- ticket: OPS-1
`,
		},
		{
			name: "additional labels and annotations",
			alerts: []slackAlert{{
				Labels: map[string]string{
					"service_name": "hidden-service",
					"error_group":  "hidden-fingerprint",
					"severity":     "ERROR",
					"team":         "payments",
				},
				Annotations: map[string]string{"owner": "on-call", "ticket": "OPS-1"},
			}},
			want: `<no value>
*Other labels:*
- severity: ERROR
- team: payments
*Other annotations:*
- owner: on-call
- ticket: OPS-1
`,
		},
		{
			name: "multiple alerts keep their own details",
			alerts: []slackAlert{
				{
					Labels: map[string]string{"severity": "ERROR", "team": "payments"},
					Annotations: map[string]string{
						"description": "First failure",
						"runbook_url": "https://example.com/first",
						"owner":       "on-call",
						"ticket":      "OPS-1",
					},
				},
				{
					Labels: map[string]string{"severity": "WARN", "team": "platform"},
					Annotations: map[string]string{
						"description": "Second failure",
						"owner":       "support",
						"ticket":      "OPS-2",
					},
				},
			},
			want: `First failure
*Actions*
- <https://example.com/first|Go to runbook>
*Other labels:*
- severity: ERROR
- team: payments
*Other annotations:*
- owner: on-call
- ticket: OPS-1
--------------------------
Second failure
*Other labels:*
- severity: WARN
- team: platform
*Other annotations:*
- owner: support
- ticket: OPS-2
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := renderSlackMessage(t, config.SlackMessage, tt.alerts)
			if message != tt.want {
				t.Errorf("Slack message = %q; want %q", message, tt.want)
			}
		})
	}
}
