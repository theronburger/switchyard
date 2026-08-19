package marketplace

type serverlessServiceSpec struct {
	ID               string
	DisplayName      string
	WorkspacePackage string
	Directory        string
	PortEnvironment  string
	Stage            string
	LambdaOffset     int
	EventBridgePort  int
	PublishedRoutes  []string
	Queues           []QueueDefinition
	UsesElasticMQ    bool
	UsesDynamoDB     bool
	SkipPreparation  bool
	NoHTTPListener   bool
}

func serverlessServiceDefinitions() []ServiceDefinition {
	specs := []serverlessServiceSpec{
		{ID: "auth-service", DisplayName: "Auth Service", PortEnvironment: "DEED_AUTH_SERVICE_PORT", NoHTTPListener: true},
		{ID: "company-service", DisplayName: "Company Service", PortEnvironment: "DEED_COMPANY_SERVICE_PORT", PublishedRoutes: []string{"DEED_COMPANY_API_URI"}, UsesElasticMQ: true, Queues: standardQueueDefinitions(
			"local-company-service-hris-import", "local-company-service-hris-import-dlq", "local-company-service-demo-events", "local-company-service-demo-events-dlq",
		)},
		{ID: "donation-batch-service", DisplayName: "Donation Batch Service", PortEnvironment: "DEED_DONATION_BATCH_SERVICE_PORT", EventBridgePort: 4090, PublishedRoutes: []string{"DEED_DONATION_BATCH_API_URI"}, UsesElasticMQ: true, Queues: append(
			standardQueueDefinitions("local-donation-batch-service-dlq", "local-donation-batch-service", "local-donation-disbursements-dlq", "local-donation-disbursements", "local-disbursements-batch-file-dlq", "local-disbursements-batch-file", "local-donation-batch-service-cut-single-disbursement-instructions", "local-donation-batch-service-cut-single-disbursement-instructions-dlq"),
			fifoQueueDefinitions(false, "local-donation-batch-service-reconciliation-payout-processing-dlq.fifo", "local-donation-batch-service-reconciliation-payout-processing.fifo")...,
		)},
		{ID: "donation-service", DisplayName: "Donation Service", PortEnvironment: "DEED_DONATION_SERVICE_PORT", EventBridgePort: 4080, PublishedRoutes: []string{"DEED_DONATION_API_URI", "DEED_DONATION_SERVICE_URI"}, UsesElasticMQ: true, Queues: donationQueueDefinitions()},
		{ID: "email-service", DisplayName: "Email Service", PortEnvironment: "DEED_EMAIL_SERVICE_PORT", EventBridgePort: 4082, PublishedRoutes: []string{"DEED_EMAIL_API_URI"}},
		{ID: "graph-service", DisplayName: "Graph Service", PortEnvironment: "DEED_GRAPH_SERVICE_PORT", LambdaOffset: 2000, PublishedRoutes: []string{"DEED_GRAPH_API_URI", "DEED_GRAPH_SERVICE_URI", "DEED_PRISMA_URI", "DEED_SLACK_SERVICE_GRAPHQL_API_URI", "DEED_WALLET_SERVICE_GRAPHQL_API_URI"}, UsesElasticMQ: true, Queues: graphQueueDefinitions()},
		{ID: "logged-time-service", DisplayName: "Logged Time Service", PortEnvironment: "DEED_LOGGED_TIME_SERVICE_PORT", EventBridgePort: 4084, PublishedRoutes: []string{"DEED_LOGGED_TIME_API_URI", "DEED_LOGGED_TIME_SERVICE_URI"}, UsesElasticMQ: true, Queues: standardQueueDefinitions(
			"local-logged-time-service-volunteer-reward-change", "local-logged-time-service-volunteer-reward-change-dlq", "local-logged-time-service-volunteer-grant-reminder", "local-logged-time-service-volunteer-grant-reminder-dlq",
		)},
		{ID: "nonprofit-service", DisplayName: "Nonprofit Service", PortEnvironment: "DEED_NONPROFIT_SERVICE_PORT", PublishedRoutes: []string{"DEED_NONPROFIT_API_URI", "DEED_NONPROFIT_SERVICE_URI"}, UsesElasticMQ: true, Queues: standardQueueDefinitions(
			"local-nonprofit-service-chapter-request-queue", "local-nonprofit-service-chapter-request-dlq", "local-nonprofit-service-chapter-request-finalize-queue", "local-nonprofit-service-chapter-request-finalize-dlq", "local-nonprofit-service-organizer-verification-queue", "local-nonprofit-service-organizer-verification-dlq", "local-nonprofit-service-process-requests", "local-nonprofit-service-process-requests-dlq", "local-nonprofit-service-chapter-request", "local-nonprofit-service-chapter-request-finalize", "local-nonprofit-service-organizer-verification",
		)},
		{ID: "notification-service", DisplayName: "Notification Service", PortEnvironment: "DEED_NOTIFICATION_SERVICE_PORT", PublishedRoutes: []string{"DEED_NOTIFICATION_API_URI"}, UsesElasticMQ: true, Queues: standardQueueDefinitions("local-notification-service-conversation-email-check", "local-notification-service-conversation-email-check-dlq")},
		{ID: "opportunity-service", DisplayName: "Opportunity Service", PortEnvironment: "DEED_OPPORTUNITY_SERVICE_PORT", PublishedRoutes: []string{"DEED_OPPORTUNITY_API_URI", "DEED_OPPORTUNITY_SERVICE_URI"}, UsesElasticMQ: true, Queues: append(
			fifoQueueDefinitions(true, "local-opportunity-service-campaign-projected-stats-dlq.fifo", "local-opportunity-service-campaign-projected-stats.fifo", "local-opportunity-service-external-demo-sync-dlq.fifo", "local-opportunity-service-external-demo-sync.fifo"),
			standardQueueDefinitions("local-opportunity-service-atlas-change-stream-dlq", "local-opportunity-service-atlas-change-stream", "local-opportunity-service-impact-goal-backfill-dlq", "local-opportunity-service-impact-goal-backfill", "local-opportunity-service-goal-evaluation-dlq", "local-opportunity-service-goal-evaluation")...,
		)},
		{ID: "payroll-service", DisplayName: "Payroll Service", PortEnvironment: "DEED_PAYROLL_SERVICE_PORT", EventBridgePort: 4088, PublishedRoutes: []string{"DEED_PAYROLL_API_URI"}, UsesElasticMQ: true, Queues: payrollQueueDefinitions()},
		{ID: "report-service", DisplayName: "Report Service", PortEnvironment: "DEED_REPORT_SERVICE_PORT", PublishedRoutes: []string{"DEED_REPORT_API_URI"}, UsesElasticMQ: true, Queues: standardQueueDefinitions("local-report-service-ai-insight-job-dlq", "local-report-service-ai-insight-job")},
		{ID: "slack-service", DisplayName: "Slack Service", PortEnvironment: "DEED_SLACK_SERVICE_PORT", EventBridgePort: 4086, PublishedRoutes: []string{"DEED_SLACK_API_URI"}, UsesElasticMQ: true, UsesDynamoDB: true, SkipPreparation: true, Queues: append(
			standardQueueDefinitions("local-slack-service-dlq"),
			fifoQueueDefinitions(true, "local-notification-service-notification-dlq.fifo", "local-slack-service-notification-dlq.fifo", "local-slack-service-notification.fifo")...,
		)},
		{ID: "time-off-service", DisplayName: "Time Off Service", PortEnvironment: "DEED_TIME_OFF_SERVICE_PORT", PublishedRoutes: []string{"DEED_TIME_OFF_API_URI", "DEED_TIME_OFF_SERVICE_URI"}, UsesElasticMQ: true, Queues: fifoQueueDefinitions(true,
			"local-time-off-service-sync-time-off-balance-dlq.fifo", "local-time-off-service-sync-time-off-balance.fifo", "local-time-off-service-sync-time-off-entries-dlq.fifo", "local-time-off-service-sync-time-off-entries.fifo",
		)},
		{ID: "wallet", DisplayName: "Wallet", WorkspacePackage: "wallet-service", Directory: "services/wallet", PortEnvironment: "DEED_WALLET_PORT", Stage: "local-dev", PublishedRoutes: []string{"DEED_WALLET_API_URI", "DEED_WALLET_URI"}, UsesElasticMQ: true, Queues: walletQueueDefinitions()},
	}
	definitions := make([]ServiceDefinition, 0, len(specs))
	for _, spec := range specs {
		definitions = append(definitions, serverlessServiceDefinition(spec))
	}
	return definitions
}

func nonprofitServiceDefinition() ServiceDefinition {
	for _, definition := range serverlessServiceDefinitions() {
		if definition.ID == "nonprofit-service" {
			return definition
		}
	}
	return ServiceDefinition{}
}

func serverlessServiceDefinition(spec serverlessServiceSpec) ServiceDefinition {
	if spec.WorkspacePackage == "" {
		spec.WorkspacePackage = spec.ID
	}
	if spec.Directory == "" {
		spec.Directory = "services/" + spec.ID
	}
	if spec.Stage == "" {
		spec.Stage = "local"
	}
	if spec.LambdaOffset == 0 {
		spec.LambdaOffset = 1000
	}
	definition := ServiceDefinition{
		ID: spec.ID, DisplayName: spec.DisplayName, Kind: ServiceKindAPI, WorkspacePackage: spec.WorkspacePackage,
		HasHTTPListener: !spec.NoHTTPListener,
		PortRequirements: []PortRequirement{
			{ID: "http", Purpose: "http", BindHost: "127.0.0.1", PreferredPortEnvironment: spec.PortEnvironment},
			{ID: "lambda", Purpose: "lambda", BindHost: "127.0.0.1", PreferredRelativeTo: "http", PreferredOffset: spec.LambdaOffset},
		},
		RunCommand: PlannedCommand{Executable: RepositoryYarnExecutable, Arguments: []string{
			"workspace", spec.WorkspacePackage, "sls:withEnv", "offline", "start", "--stage", spec.Stage, "--config", ".switchyard.serverless.ts",
		}, WorkingDirectory: "."},
		EnvironmentBindings: []EnvironmentBinding{{Name: spec.PortEnvironment, PortRequirement: "http", Format: EnvironmentValueDecimalPort}},
		Readiness:           []Probe{{Kind: ProbeKindTCP, PortRequirement: "http"}, {Kind: ProbeKindTCP, PortRequirement: "lambda"}},
		Health:              []Probe{{Kind: ProbeKindHTTP, PortRequirement: "http", Method: "GET", Path: "/", AcceptedStatuses: []HTTPStatusRange{{Minimum: 200, Maximum: 499}}}},
		Queues:              append([]QueueDefinition(nil), spec.Queues...),
		ServerlessOverlay: &ServerlessOverlay{Directory: spec.Directory, Filename: ".switchyard.serverless.ts", SourceConfig: "serverless.ts", Overrides: []ServerlessOverride{
			{ConfigurationPath: []string{"custom", "serverless-offline", "host"}, PortRequirement: "http", Format: OverlayValueLoopback},
			{ConfigurationPath: []string{"custom", "serverless-offline", "httpPort"}, PortRequirement: "http", Format: OverlayValueIntegerPort},
			{ConfigurationPath: []string{"custom", "serverless-offline", "lambdaPort"}, PortRequirement: "lambda", Format: OverlayValueIntegerPort},
		}},
	}
	if spec.NoHTTPListener {
		definition.Readiness = []Probe{{Kind: ProbeKindTCP, PortRequirement: "lambda"}}
		definition.Health = []Probe{{Kind: ProbeKindTCP, PortRequirement: "lambda"}}
	}
	if !spec.SkipPreparation {
		definition.PrepareCommands = []PlannedCommand{{Executable: RepositoryYarnExecutable, Arguments: []string{
			"turbo", "run", "build:no-dependencies", "--filter=" + spec.WorkspacePackage, "--ui=stream",
		}, WorkingDirectory: "."}}
	}
	for _, route := range spec.PublishedRoutes {
		definition.PublishedRoutes = append(definition.PublishedRoutes, EnvironmentBinding{Name: route, PortRequirement: "http", Format: EnvironmentValueHTTPURL})
	}
	if spec.EventBridgePort != 0 {
		definition.PortRequirements = append(definition.PortRequirements,
			PortRequirement{ID: "eventbridge", Purpose: "eventbridge", BindHost: "127.0.0.1", PreferredPort: spec.EventBridgePort},
			PortRequirement{ID: "eventbridge-pubsub", Purpose: "eventbridge-pubsub", BindHost: "127.0.0.1", PreferredRelativeTo: "eventbridge", PreferredOffset: 1},
		)
		definition.Readiness = append(definition.Readiness, Probe{Kind: ProbeKindTCP, PortRequirement: "eventbridge"}, Probe{Kind: ProbeKindTCP, PortRequirement: "eventbridge-pubsub"})
		definition.ServerlessOverlay.Overrides = append(definition.ServerlessOverlay.Overrides,
			ServerlessOverride{ConfigurationPath: []string{"custom", "serverless-offline-aws-eventbridge", "port"}, PortRequirement: "eventbridge", Format: OverlayValueIntegerPort},
			ServerlessOverride{ConfigurationPath: []string{"custom", "serverless-offline-aws-eventbridge", "pubSubPort"}, PortRequirement: "eventbridge-pubsub", Format: OverlayValueIntegerPort},
		)
	}
	if spec.UsesElasticMQ {
		definition.PortRequirements = append(definition.PortRequirements,
			PortRequirement{ID: "elasticmq-rest", Purpose: "elasticmq-rest", BindHost: "127.0.0.1", PreferredPort: 9324},
			PortRequirement{ID: "elasticmq-ui", Purpose: "elasticmq-ui", BindHost: "127.0.0.1", PreferredRelativeTo: "elasticmq-rest", PreferredOffset: 1},
		)
		definition.Infrastructure = append(definition.Infrastructure, InfrastructureRequirement{
			ID: "elasticmq", DisplayName: "ElasticMQ", Kind: "container", Scope: EnvironmentInfrastructureScope,
			Image: "softwaremill/elasticmq", Dedicated: true,
			Ports:     []ContainerPort{{PortRequirement: "elasticmq-rest", ContainerPort: 9324}, {PortRequirement: "elasticmq-ui", ContainerPort: 9325}},
			Readiness: []Probe{{Kind: ProbeKindTCP, PortRequirement: "elasticmq-rest"}},
		})
		definition.ServerlessOverlay.Overrides = append(definition.ServerlessOverlay.Overrides,
			ServerlessOverride{ConfigurationPath: []string{"custom", "serverless-offline-sqs", "endpoint"}, PortRequirement: "elasticmq-rest", Format: OverlayValueHTTPURL},
		)
	}
	if spec.UsesDynamoDB {
		definition.PortRequirements = append(definition.PortRequirements, PortRequirement{ID: "dynamodb", Purpose: "dynamodb", BindHost: "127.0.0.1", PreferredPort: 8000})
		definition.Infrastructure = append(definition.Infrastructure, InfrastructureRequirement{
			ID: "dynamodb", DisplayName: "DynamoDB Local", Kind: "container", Scope: EnvironmentInfrastructureScope,
			Image: "amazon/dynamodb-local", Dedicated: true,
			Ports:     []ContainerPort{{PortRequirement: "dynamodb", ContainerPort: 8000}},
			Readiness: []Probe{{Kind: ProbeKindTCP, PortRequirement: "dynamodb"}},
		})
	}
	if spec.ID == "report-service" {
		definition.ServerlessOverlay.Plugins = []string{"serverless-offline-sqs"}
		definition.ServerlessOverlay.Overrides = append(definition.ServerlessOverlay.Overrides, ServerlessOverride{
			ConfigurationPath: []string{"provider", "environment", "AI_INSIGHT_JOB_QUEUE_URL"},
			PortRequirement:   "elasticmq-rest", Format: OverlayValueHTTPURL,
			URLPath: "/queue/local-report-service-ai-insight-job",
		})
	}
	return definition
}

func standardQueueDefinitions(names ...string) []QueueDefinition {
	queues := make([]QueueDefinition, 0, len(names))
	for _, name := range names {
		queues = append(queues, QueueDefinition{Name: name})
	}
	return queues
}

func fifoQueueDefinitions(contentBasedDeduplication bool, names ...string) []QueueDefinition {
	queues := make([]QueueDefinition, 0, len(names))
	for _, name := range names {
		queues = append(queues, QueueDefinition{Name: name, FIFO: true, ContentBasedDeduplication: contentBasedDeduplication})
	}
	return queues
}

func graphQueueDefinitions() []QueueDefinition {
	queues := fifoQueueDefinitions(true,
		"local-graph-service-campaign-action-reminder-dlq.fifo", "local-graph-service-campaign-action-reminder.fifo", "local-graph-service-badge-trigger-dlq.fifo", "local-graph-service-badge-trigger.fifo", "local-graph-service-community-user-dlq.fifo", "local-graph-service-community-user.fifo", "local-graph-service-community-batch-dlq.fifo", "local-graph-service-community-batch.fifo", "local-donation-change-events.fifo", "local-graph-service-action-reminder-dlq.fifo", "local-graph-service-action-reminder.fifo",
	)
	queues = append(queues, standardQueueDefinitions("local-graph-service-challenge-contributions-dlq", "local-graph-service-challenge-contributions", "local-graph-service-user-metrics-aggregation-dlq", "local-graph-service-user-metrics-aggregation", "local-graph-service-atlas-change-stream-dlq", "local-graph-service-atlas-change-stream")...)
	queues = append(queues, fifoQueueDefinitions(false, "local-graph-service-challenge-impact-dlq.fifo", "local-graph-service-challenge-impact.fifo", "local-graph-service-challenge-stats-recalculation-dlq.fifo", "local-graph-service-challenge-stats-recalculation.fifo")...)
	return queues
}

func donationQueueDefinitions() []QueueDefinition {
	queues := fifoQueueDefinitions(true,
		"local-donation-schedule-recurring-dlq.fifo", "local-donation-schedule-recurring-execution.fifo", "local-donation-change-events.fifo", "local-donation-change-events-dlq.fifo",
	)
	queues = append(queues, standardQueueDefinitions(
		"local-donation-service-donation-schedule-atlas-intake", "local-donation-service-donation-schedule-atlas-intake-dlq",
	)...)
	queues = append(queues,
		QueueDefinition{Name: "local-donation-service-donation-schedule-signal-dlq.fifo", FIFO: true},
		QueueDefinition{Name: "local-donation-service-donation-schedule-signal.fifo", FIFO: true, ContentBasedDeduplication: true},
	)
	return queues
}

func payrollQueueDefinitions() []QueueDefinition {
	queues := standardQueueDefinitions(
		"local-payroll-service-generate-deductions", "local-payroll-service-generate-deductions-dlq", "local-payroll-service-transfer-deductions", "local-payroll-service-transfer-deductions-dlq", "local-payroll-service-reconcile-deductions", "local-payroll-service-reconcile-deductions-dlq", "local-payroll-donation-disbursability", "local-payroll-donation-disbursability-dlq", "local-payroll-service-orchestrate-payroll-run-import", "local-payroll-service-orchestrate-payroll-run-import-dlq", "local-payroll-service-deduction-notifications", "local-payroll-service-deduction-notifications-dlq", "local-payroll-service-dlq", "local-payroll-service", "local-payroll-donation-schedules", "local-payroll-donation-schedules-dlq", "local-payroll-donation-schedules-publisher-dlq",
	)
	return append(queues, fifoQueueDefinitions(true, "local-payroll-service-stage-deductions-generation.fifo", "local-payroll-service-stage-deductions-generation-dlq.fifo", "local-donation-change-events.fifo")...)
}

func walletQueueDefinitions() []QueueDefinition {
	queues := fifoQueueDefinitions(true,
		"local-dev-wallet-service-dead-letter.fifo", "local-dev-wallet-service-withdrawal.fifo", "local-dev-wallet-service-volunteer-time-donation-credit.fifo", "local-dev-wallet-service-donation-credit-reminder-dlq.fifo", "local-dev-wallet-service-donation-credit-reminder.fifo", "local-dev-wallet-service-award-donation-credit-seed-dlq.fifo", "local-dev-wallet-service-award-donation-credit-seed.fifo",
	)
	return append(queues, standardQueueDefinitions(
		"local-dev-wallet-service-atlas-change-stream-dlq", "local-dev-wallet-service-atlas-change-stream", "local-dev-wallet-service-budget-limit-projections-dlq", "local-dev-wallet-service-budget-limit-projections", "local-dev-wallet-service-wallet-transaction-accounting-projections-dlq", "local-dev-wallet-service-wallet-transaction-accounting-projections", "local-dev-wallet-service-wallet-median-donation-projections-dlq", "local-dev-wallet-service-wallet-median-donation-projections",
	)...)
}
