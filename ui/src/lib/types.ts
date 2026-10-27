// API envelope
export interface StandardResponse<T> {
  data: T;
  error: string | null;
}

// Core trace data structures
export interface Log {
  timestamp: number;
  fields: Record<string, any>;
}

export interface Span {
  traceId: string;
  spanId: string;
  parentSpanId: string | null;
  operationName: string;
  startTime: number; // microseconds
  duration: number; // microseconds
  tags: Record<string, any>;
  logs: Log[];
  children: Span[];
}

export interface Trace {
  traceId: string;
  rootSpans: Span[];
  allSpans: Span[];
}

// ADK Invocation structures
export interface Content {
  role: string;
  parts: Part[];
}

export interface Part {
  text?: string;
  functionCall?: FunctionCall;
  functionResponse?: FunctionResponse;
}

export interface FunctionCall {
  name: string;
  args: Record<string, any>;
  id?: string;
}

export interface FunctionResponse {
  name: string;
  response: Record<string, any>;
  id?: string;
}

export interface ToolCall {
  name: string;
  args: Record<string, any>;
  id?: string;
}

export interface ToolResponse {
  name: string;
  response: Record<string, any>;
  id?: string;
}

export interface IntermediateData {
  toolUses: ToolCall[];
  toolResponses: ToolResponse[];
}

export interface Invocation {
  invocationId: string;
  userContent: Content;
  finalResponse: Content;
  intermediateData: IntermediateData;
  creationTimestamp?: number;
}

// Trace conversion API response types
export interface TraceConversionMetadata {
  agentName?: string;
  model?: string;
  startTime?: number;
  userInputPreview?: string;
  finalOutputPreview?: string;
  sessionName?: string;
}

export interface TraceConversionEntry {
  traceId: string;
  invocations: Invocation[];
  warnings: string[];
  metadata: TraceConversionMetadata;
}

export interface ConvertTracesResponse {
  traces: TraceConversionEntry[];
}

// Evaluation results
export type EvalStatus = 'PASSED' | 'FAILED' | 'NOT_EVALUATED' | 'ERROR';

export interface ToolCallComparison {
  name: string;
  args: Record<string, any>;
}

export interface TrajectoryComparison {
  invocationId: string | null;
  expected: ToolCallComparison[];
  actual: ToolCallComparison[];
  matched: boolean;
}

export interface MetricDetails {
  comparisons?: TrajectoryComparison[];
  // per_invocation is hallucinations_v1-specific: one entry per
  // invocation (same shape/key as the persisted Runs details blob
  // below - PersistedHallucinationInvocation/HallucinationSentence - so
  // the same rendering logic works for both the live /api/evaluate
  // response and GET /api/runs/{id}/results).
  per_invocation?: PersistedHallucinationInvocation[];
  // jev_* metrics (internal/decision): the question Jev was asked, its
  // type and passing answer, and per turn what Jev saw and answered.
  question?: string;
  question_type?: 'noul' | 'choice';
  expect?: boolean | string;
  turns?: JevTurn[];
}

export interface JevTurn {
  invocation_id: string;
  user_input: string;
  final_response: string;
  score: number | null;
  answer?: JevAnswer;
}

export interface JevAnswer {
  type: string;
  noul?: number;
  choice?: string;
  probabilities?: Record<string, number>;
  confidence?: number;
}

export type EvaluatorKind = 'algorithm' | 'llm_judge' | 'vertex_judge';

export interface MetricResult {
  metricName: string;
  score: number | null;
  evalStatus: EvalStatus;
  perInvocationScores: (number | null)[];
  error: string | null;
  details?: MetricDetails | null;
  // evaluatorKind/judgeModel explain how the score was produced: a
  // deterministic algorithm, or a live LLM-as-judge call (judgeModel
  // names the model), or Vertex AI's Managed Eval Service.
  evaluatorKind: EvaluatorKind;
  judgeModel?: string;
}

export interface PerformanceMetrics {
  latency: {
    overall: { p50: number; p95: number; p99: number };
    llmCalls: { p50: number; p95: number; p99: number };
    toolExecutions: { p50: number; p95: number; p99: number };
  };
  tokens: {
    totalPrompt: number;
    totalOutput: number;
    total: number;
    perLlmCall: { p50: number; p95: number; p99: number };
    cacheCreationTokens?: number;
    cacheReadTokens?: number;
  };
}

export interface TraceResult {
  traceId: string;
  sessionId?: string;
  numInvocations: number;
  metricResults: MetricResult[];
  conversionWarnings: string[];
  performanceMetrics?: PerformanceMetrics;
  agentName?: string;
  agentId?: string;
  model?: string;
  responseModel?: string;
  provider?: string;
  startTime?: number;
  userInputPreview?: string;
  finalOutputPreview?: string;
}

export interface RunResultPerformanceMetrics {
  tokens: {
    totalPrompt: number;
    totalOutput: number;
    total: number;
    avgPerTrace: {
      prompt: number;
      output: number;
    };
  };
  traceCount: number;
}

export interface RunResult {
  traceResults: TraceResult[];
  errors: string[];
  performanceMetrics?: RunResultPerformanceMetrics;
}

// Table-specific types
export type TraceRowStatus = 'pending' | 'loading' | 'complete' | 'error';

export interface TraceTableRow {
  traceId: string;
  sessionId?: string;
  status: TraceRowStatus;
  agentName?: string;
  agentId?: string;
  startTime?: number;
  model?: string;
  responseModel?: string;
  provider?: string;
  userInputPreview?: string;
  finalOutputPreview?: string;
  metricResults: Map<string, MetricResult>;
  numInvocations?: number;
  invocations?: Invocation[];
  conversionWarnings: string[];
  error?: string;
  performanceMetrics?: PerformanceMetrics;
}

// Evaluation API config
export interface EvalConfig {
  evaluators: Array<{
    type: 'builtin';
    name: string;
    judgeModel?: string;
    threshold?: number;
    trajectoryMatchType?: string;
    // The rubric texts of a rubric metric, from the chosen rubric set.
    rubrics?: string[];
  }>;
}

// A saved list of rubric texts for one of the two rubric metrics (GET /api/rubricsets).
export type RubricKind = 'final_response' | 'tool_use';

export interface RubricSet {
  rubricSetId: string;
  name: string;
  description?: string;
  kind: RubricKind;
  rubrics: string[];
  createdAt: string;
  updatedAt: string;
  // Who saved the set first; only they, or an admin, change or delete it.
  createdBy?: string;
}

// EvalSet types
export interface EvalSetMetadata {
  eval_set_id: string;
  name: string;
  description: string;
}

export interface EvalCase {
  eval_id: string;
  conversation: Invocation[];
}

export interface EvalSet {
  eval_set_id: string;
  name: string;
  description: string;
  eval_cases: EvalCase[];
}

// Persisted eval set summary (durable storage, GET /api/evalsets). The
// full EvalSet (with every eval case's conversation) is only fetched via
// GET /api/evalsets/{id} when actually opening one - the list view only
// needs these lightweight fields.
export interface EvalSetSummary {
  evalSetId: string;
  name: string;
  description?: string;
  numCases: number;
  createdAt: string;
  updatedAt: string;
}

// One cap or limit the server runs with (GET /api/usage/limits). limit 0 means none; used is absent when not counted.
export interface LimitItem {
  key: string;
  label: string;
  group: string;
  limit: number;
  used?: number;
  unit: 'tokens' | 'calls' | 'count' | 'days' | 'bytes';
  note?: string;
}

// View types
export type ViewType = 'upload' | 'dashboard' | 'inspector' | 'comparison' | 'builder' | 'streaming' | 'runs' | 'usage' | 'quality' | 'settings';

// Persisted run history (durable storage, GET /api/runs)
export type RunStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled';

export interface ResultCounts {
  passed: number;
  failed: number;
  errored: number;
  skipped: number;
}

// One entry per evaluator in summary.per_metric. avg_score is null when no
// invocation of that evaluator produced a numeric score (e.g. it only errored).
export interface PerMetricSummary extends ResultCounts {
  avg_score: number | null;
}

// summary is a free-form dict on the backend, so its keys stay snake_case on the wire.
export interface RunSummary {
  trace_count: number;
  result_counts: ResultCounts;
  per_metric?: Record<string, PerMetricSummary>;
  agents?: string[];
  errors?: string[];
  models?: string[];
  duration_ms?: number;
}

// spec.evalSet/evalConfig are stored as raw dicts, so their inner keys keep
// the original snake_case (e.g. ADK eval_set_id) rather than being camelCased.
export interface RunEvaluatorConfig {
  name?: string;
  type?: string;
  threshold?: number;
  judge_model?: string;
  trajectory_match_type?: string;
  rubrics?: string[];
}

export interface RunSpecSummary {
  approach?: string;
  evalSet?: { eval_set_id?: string; name?: string; eval_cases?: unknown[] } | null;
  evalConfig?: { evaluators?: RunEvaluatorConfig[] } & Record<string, unknown>;
  target?: { kind?: string; trace_count?: number; trace_files?: string[] } & Record<string, unknown>;
}

// One persisted Result row (GET /api/runs/{id}/results). Top-level fields are
// camelCased by the API; the free-form `details` keeps snake_case inner keys.
export type ResultStatus2 = 'passed' | 'failed' | 'errored' | 'skipped';

export interface PersistedComparison {
  invocation_id?: string | null;
  matched?: boolean;
  expected?: ToolCallComparison[];
  actual?: ToolCallComparison[];
}

export interface HallucinationSentence {
  sentence: string;
  label: string;
  rationale?: string | null;
  supporting_excerpt?: string | null;
  contradicting_excerpt?: string | null;
}

export interface PersistedHallucinationInvocation {
  invocation_id: string | null;
  score: number | null;
  sentences: HallucinationSentence[];
}

// rubric_based_*_v1: one verdict per rubric, majority-voted over samples.
export interface RubricVerdict {
  id: string;
  rubric: string;
  verdict: 'yes' | 'no' | null;
  rationale?: string;
}

export interface RubricInvocation {
  invocation_id?: string | null;
  score: number | null;
  rubrics: RubricVerdict[];
}

export interface ResultDetails {
  comparisons?: PersistedComparison[];
  per_invocation?: PersistedHallucinationInvocation[];
  rubric_invocations?: RubricInvocation[];
  // jev_* results (see MetricDetails).
  question?: string;
  question_type?: 'noul' | 'choice';
  expect?: boolean | string;
  turns?: JevTurn[];
  [key: string]: unknown;
}

export interface RunResultRow {
  resultId: string;
  runId: string;
  evalSetItemId: string;
  evalSetItemName: string;
  evaluatorName: string;
  evaluatorType: string;
  status: ResultStatus2;
  score: number | null;
  perInvocationScores: (number | null)[];
  traceId?: string | null;
  spanId?: string | null;
  details?: ResultDetails;
  errorText?: string | null;
  tokensUsed?: Record<string, unknown> | null;
  latencyMs?: number | null;
  createdAt: string;
}

export interface Run {
  runId: string;
  status: RunStatus;
  spec: RunSpecSummary;
  attempt?: number;
  workerId?: string | null;
  error?: string | null;
  summary?: RunSummary | null;
  createdAt: string;
  startedAt?: string | null;
  finishedAt?: string | null;
  cancelRequested?: boolean;
}

// Metric metadata type
export interface MetricMetadata {
  name: string;
  category: string;
  requiresEvalSet?: boolean;
  requiresLLM?: boolean;
  requiresGCP?: boolean;
  requiresRubrics?: boolean;
  working?: boolean;
  description: string;
}

// Available metrics (from ADK) - Default fallback
// Note: In production, these should be loaded from the API /metrics endpoint
export const AVAILABLE_METRICS: MetricMetadata[] = [
  // Trajectory metrics
  {
    name: 'tool_trajectory_avg_score',
    category: 'trajectory',
    requiresEvalSet: true,
    requiresLLM: false,
    requiresGCP: false,
    requiresRubrics: false,
    working: true,
    description: 'Compare tool call sequences against expected trajectory'
  },
  // Response metrics
  {
    name: 'response_match_score',
    category: 'response',
    requiresEvalSet: true,
    requiresLLM: false,
    requiresGCP: false,
    requiresRubrics: false,
    working: true,
    description: 'Text similarity between actual and expected responses using ROUGE-1'
  },
  {
    name: 'response_evaluation_score',
    category: 'response',
    requiresEvalSet: true,
    requiresLLM: false,
    requiresGCP: true,
    requiresRubrics: false,
    working: true,
    description: 'Semantic evaluation of response quality using Vertex AI'
  },
  {
    name: 'final_response_match_v2',
    category: 'response',
    requiresEvalSet: true,
    requiresLLM: true,
    requiresGCP: false,
    requiresRubrics: false,
    working: true,
    description: 'LLM-based comparison of final responses'
  },
  // Quality metrics
  {
    name: 'rubric_based_final_response_quality_v1',
    category: 'quality',
    requiresEvalSet: false,
    requiresLLM: true,
    requiresGCP: false,
    requiresRubrics: true,
    working: false,
    description: 'Rubric-based quality assessment of responses (requires rubrics config)'
  },
  {
    name: 'rubric_based_tool_use_quality_v1',
    category: 'quality',
    requiresEvalSet: false,
    requiresLLM: true,
    requiresGCP: false,
    requiresRubrics: true,
    working: false,
    description: 'Rubric-based assessment of tool usage quality (requires rubrics config)'
  },
  // Safety metrics
  {
    name: 'hallucinations_v1',
    category: 'safety',
    requiresEvalSet: false,
    requiresLLM: true,
    requiresGCP: false,
    requiresRubrics: false,
    working: true,
    description: 'Detect hallucinated information in responses'
  },
  {
    name: 'safety_v1',
    category: 'safety',
    requiresEvalSet: false,
    requiresLLM: false,
    requiresGCP: true,
    requiresRubrics: false,
    working: true,
    description: 'Safety and security assessment using Vertex AI'
  },
  // Multi-turn metrics (Vertex AI Gen AI Eval SDK)
  {
    name: 'multi_turn_task_success_v1',
    category: 'multi-turn',
    requiresEvalSet: false,
    requiresLLM: false,
    requiresGCP: true,
    requiresRubrics: false,
    working: true,
    description: 'Evaluates if the agent achieved the goal(s) of the multi-turn conversation (Vertex AI)'
  },
  {
    name: 'multi_turn_trajectory_quality_v1',
    category: 'multi-turn',
    requiresEvalSet: false,
    requiresLLM: false,
    requiresGCP: true,
    requiresRubrics: false,
    working: true,
    description: 'Evaluates the overall trajectory the agent took across the conversation (Vertex AI)'
  },
  {
    name: 'multi_turn_tool_use_quality_v1',
    category: 'multi-turn',
    requiresEvalSet: false,
    requiresLLM: false,
    requiresGCP: true,
    requiresRubrics: false,
    working: true,
    description: 'Evaluates function calls made during a multi-turn conversation (Vertex AI)'
  }
];

// Streaming / Live session types
export interface ConversationElement {
  type: 'user_input' | 'tool_call' | 'tool_result' | 'agent_response';
  timestamp: number;
  invocationId: string;
  data: any;
}

export interface StreamingInvocation {
  invocationId: string;
  userText: string;
  agentText: string;
  toolCalls: Array<{ name: string; args: any; id?: string; latencyMs?: number; resultBytes?: number }>;
  toolResponses?: Array<{ name: string; response: Record<string, any>; id?: string }>;
  modelInfo?: {
    models?: string[];
    inputTokens?: number;
    outputTokens?: number;
    /** Wall-clock span of this turn's spans (earliest start to latest end), in ms. */
    turnLatencyMs?: number;
    /** When the turn's earliest span started, Unix ms. A session grows over time, so each turn has its own. */
    turnStartedAt?: number;
    provider?: string;
  };
}

export interface LiveSession {
  sessionId: string;
  traceId: string;
  evalSetId: string | null;
  spans: any[];
  status: 'active' | 'complete';
  metadata: Record<string, any>;
  invocations?: StreamingInvocation[];
  liveElements: ConversationElement[];
  liveStats: {
    totalInputTokens: number;
    totalOutputTokens: number;
    model?: string;
  };
  startedAt: string;
  completedAt?: string | null;
  // The trace's root span and how many LLM calls it
  // made, known once the session is complete; errors counts its spans with
  // status ERROR.
  rootSpanName?: string;
  llmCalls?: number;
  errors?: number;
  // The prompts of a Claude Code session, which has no invocations to count.
  turns?: number;
  // The parts of the session's input tokens read from and written to the prompt cache.
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
  // Distinct gen_ai.agent.name and model values on the session's spans, in the order first seen.
  agents?: string[];
  // Tenants of the session: the sender's tenant, or the tenant attribute values on its spans.
  tenants?: string[];
  models?: string[];
  // Span window (earliest span start to latest span end), not ingest time.
  durationMs?: number;
  // Online evaluation scored it: the card shows judge scores a person can label.
  scored?: boolean;
  // The sender's own id for the work this session is a part of, and its 1-based number in that thread. An always-on
  // agent keeps one thread for weeks; ego cuts it into sessions (parts) of whole traces, the first under the
  // thread's id and the n-th as `<thread>~n`.
  thread?: string;
  part?: number;
}

// One row of GET /api/activity: an agent in a tenant whose sessions are made of turns.
export interface ActivitySubject {
  agent: string;
  tenant?: string;
  sessions: number;
  turns: number;
  firstSeen: string;
  lastSeen: string;
}

// One turn of GET /api/activity/turns: the work an agent did for one prompt or wake, summarized.
export interface TurnRow {
  session: string;
  part?: number;
  traceId: string;
  rootSpanId: string;
  operation: string;
  startMs: number;
  durationMs: number;
  failed?: boolean;
  failedTools?: number;
  requests: number;
  tools: number;
  modelMs: number;
  toolMs: number;
  approvalMs: number;
  inputTokens: number;
  cacheReadTokens?: number;
  outputTokens: number;
  promptChars?: number;
  toolCalls?: Record<string, TurnCount>;
  modelCalls?: Record<string, TurnCount>;
  // What followed what in the turn, named as the graph names its nodes.
  flow?: { from: string; to: string; n: number }[];
}

// How often a tool or a model was called in a turn, how often it failed and how long the calls took; server names the
// MCP server of a tool (empty for a built-in tool and for a model).
export interface TurnCount {
  calls: number;
  failed?: number;
  ms?: number;
  server?: string;
}

export interface TurnSegment {
  session: string;
  part?: number;
  startMs: number;
  endMs: number;
}

export interface TurnsResult {
  agent: string;
  tenant?: string;
  fromMs: number;
  toMs: number;
  segments: TurnSegment[];
  turns: TurnRow[];
  truncated?: boolean;
}

// Inspector-specific types
export type ExtractionType = 'user_input' | 'tool_use' | 'tool_response' | 'final_response';

export interface ExtractionInfo {
  type: ExtractionType;
  invocationId: string;
  spanId: string;
  tagPath: string;
  jsonPath: string;
  lineRange: { start: number; end: number };
}

export interface SpanReference {
  spanId: string;
  extractionType: ExtractionType;
  tagKey: string;
}

export interface TraceMapping {
  spanToData: Map<string, ExtractionInfo[]>;
  dataToSpan: Map<string, SpanReference>;
  jsonLineRanges: Map<string, { start: number; end: number }>;
}

export interface InspectorUIState {
  selectedInvocationId: string | null;
}

// Trace editor types
export type TraceFileFormat = 'jaeger' | 'otlp-jsonl';

export interface SpanLocationRef {
  traceIndex?: number;
  spanIndex?: number;
  lineIndex?: number;
}

export interface ParsedTraceFile {
  format: TraceFileFormat;
  fileName: string;
  rawData: any;
  spanIndex: Map<string, SpanLocationRef>;
}

export interface SpanEditMapping {
  invocationId: string;
  format: 'adk' | 'genai';
  userInputSpanId: string;
  finalResponseSpanId: string;
  toolSpanIds: string[];
  userInputAttrKey: string;
  finalResponseAttrKey: string;
}

// Token usage ledger (GET /api/usage, GET /api/usage/calls). One cell per (time bucket, service, tenant, call kind, model).
export interface UsageBucket {
  start: number;
  service: string;
  tenant: string;
  kind: string;
  model: string;
  calls: number;
  errors: number;
  inputTokens: number;
  outputTokens: number;
  // The parts of inputTokens read from and written to the prompt cache.
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
  durationMs: number;
}

export interface UsageSeries {
  from: number;
  to: number;
  bucketMs: number;
  buckets: UsageBucket[];
}

// One row of GET /api/usage/calls?group=session: a session's calls on one
// model, summed.
export interface UsageSession {
  sessionId: string;
  start: number;
  end: number;
  calls: number;
  errors: number;
  kinds: string;
  tenants: string;
  model: string;
  inputTokens: number;
  outputTokens: number;
  peakTokens: number;
  durationMs: number;
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
}

export interface UsageCall {
  spanId: string;
  traceId: string;
  sessionId: string;
  start: number;
  durationMs: number;
  service: string;
  tenant: string;
  kind: string;
  model: string;
  name: string;
  inputTokens: number;
  outputTokens: number;
  isError: boolean;
}

// Online evaluation (GET /api/online/*). See
// docs/online-evaluation.md.
export interface OnlineCost {
  judgeTokens: number;
  jevCalls: number;
}

export interface OnlineStatus {
  profiles: string[];
  budget: {
    day: string;
    caps: { judgeTokensPerDay: number; jevCallsPerDay: number };
    spent: OnlineCost;
    reserved: OnlineCost;
  };
  queue: number;
  counts: Record<string, number>;
  // Sessions scored in the last 24 hours, counted from the store; absent on a server from before it was added.
  sessionsScored24h?: number;
  openAlerts: number;
  sinks: string[];
  // Absent on a server from before findings and labels.
  findings?: {
    enabled: boolean;
    written: number;
    skipped: { budget: number; noEvidence: number; uncalibrated: number };
    tokens: number;
  };
  labels?: OnlineLabelsStatus;
}

// Judge agreement per metric over the last 7 days (docs/rsi.md, section 4).
export interface OnlineLabelsMetric {
  agree: number;
  disagree: number;
  // null when the window holds no labels.
  agreement: number | null;
  // True when people disagree with this metric's judge too often for it to drive findings.
  gated: boolean;
  // Mean absolute difference between the last anchor re-score and the labelled scores.
  drift?: number;
  driftN?: number;
  driftAt?: string;
}

export interface OnlineLabelsStatus {
  perMetric: Record<string, OnlineLabelsMetric>;
  skipped: { budget: number };
}

export interface OnlineSeriesPoint {
  bucketMs: number;
  tenant: string;
  kind: string;
  metric: string;
  mean: number;
  n: number;
}

export interface OnlineAlert {
  id: string;
  key: { tenant: string; kind: string; metric: string };
  status: 'open' | 'resolved';
  openedAt: string;
  updatedAt: string;
  resolvedAt?: string;
  last: {
    baseMean: number;
    curMean: number;
    nBase: number;
    nCur: number;
    drop: number;
    suspect?: string;
    worst?: string[];
  };
}

// Findings and corrections (GET /api/online/findings, /corrections). The wire shape is snake_case, as in the MCP
// tools. See docs/rsi.md.
export type OnlineFindingState = 'open' | 'corrected' | 'resolved' | 'stale';
export type OnlineCorrectionState = 'pending' | 'verified' | 'failed';

export interface OnlineVerificationWindow {
  mean: number;
  n: number;
}

export interface OnlineVerification {
  before: OnlineVerificationWindow;
  after: OnlineVerificationWindow;
  window_end: string;
  reason?: string;
}

export interface OnlineFinding {
  finding_id: string;
  tenant: string;
  kind: string;
  metric: string;
  state: OnlineFindingState;
  alert_id?: string;
  created_at: string;
  updated_at: string;
  diagnosis: {
    failure_mode: string;
    summary: string;
    suggested_correction: string;
    confidence: 'low' | 'medium' | 'high';
    evidence: { session_id: string; excerpt: string; why: string }[];
  };
  sessions: string[];
  window: { from: string; to: string; n: number; mean: number; below: number };
  model: string;
  model_error?: string;
  // How many evidence sessions a person scored or confirmed.
  confirmed?: number;
  history: {
    correction_id: string;
    what: string;
    version?: string;
    by?: string;
    state: OnlineCorrectionState;
    at: string;
  }[];
}

export interface OnlineCorrection {
  correction_id: string;
  finding_id?: string;
  tenant: string;
  kind: string;
  metric?: string;
  version?: string;
  state: OnlineCorrectionState;
  created_at: string;
  verified_at?: string;
  what: string;
  by?: string;
  verification: OnlineVerification;
}

// A person's verdict on a judge's score (POST/GET /api/online/labels).
export interface OnlineLabel {
  label_id: string;
  session_id: string;
  tenant: string;
  kind: string;
  metric: string;
  judge_score: number;
  verdict: 'agree' | 'disagree';
  score?: number;
  note?: string;
  by?: string;
  created_at: string;
}

export interface OnlineLabelInput {
  sessionId: string;
  metric: string;
  verdict: 'agree' | 'disagree';
  score?: number;
  note?: string;
}

// One metric of GET /api/online/sessions/{id}/explain, reduced to what the labels need.
export interface OnlineSessionScore {
  metric: string;
  score: number | null;
  status: string;
  error?: string;
  // The judge's reason for the first failing item, and how many of the items failed. Which list is filled depends
  // on the metric: sentences (hallucinations_v1), rubrics, comparisons (tool_trajectory), decisions (jev_*).
  rationale?: string;
  failing?: number;
  total?: number;
  truncated?: number;
  sentences?: { invocation_id?: string; sentence: string; label: string; rationale?: string; supporting_excerpt?: string; contradicting_excerpt?: string }[];
  rubrics?: { invocation_id?: string; id?: string; rubric: string; verdict: string; rationale?: string }[];
  comparisons?: { invocation_id?: string; matched: boolean; expected: { tool: string; args?: Record<string, unknown> }[]; actual: { tool: string; args?: Record<string, unknown> }[] }[];
  decisions?: { invocation_id?: string; question?: string; user_input?: string; final_response?: string; score: number | null; answer?: string }[];
  // The newest label's verdict and the score the person gave with it; that score is the score of record.
  human_verdict?: 'agree' | 'disagree';
  human_score?: number;
}

/** One scoring job of the online loop: the sessions of a kind, what scores them and how many. */
export interface OnlineJob {
  /** A call kind, or a pattern such as `*-spot`. */
  kind: string;
  service?: string;
  tenant?: string;
  /** How many of the last turns of a session are scored. */
  maxInvocations?: number;
  /** Sessions of each tenant and kind scored per hour; absent takes the default rate. */
  perHour?: number;
  evaluators: { name: string; rubrics?: string[]; numSamples?: number; judgeModel?: string }[];
}

export interface OnlineJobs {
  perKindPerHour: number;
  profiles: OnlineJob[];
  /** `stored` once jobs were saved here, `file` while the server's own configuration is in force. */
  source: 'stored' | 'file';
  /** Kinds that sent sessions with no job to score them. */
  unscored: { kind: string; sessions: number }[];
}
