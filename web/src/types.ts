// Types of the web API (internal/server, internal/store, internal/api).
// Durations are Go duration strings such as "4h0m0s"; times are RFC 3339.

export type Duration = string

export interface User {
  id: number
  username: string
  must_change_password: boolean
  disabled: boolean
  last_login_at?: string
  created_at: string
}

export interface List<T> {
  items: T[]
  total: number
}

export type JobType = 'full' | 'incr'
export type JobStatus = 'pending' | 'running' | 'success' | 'warning' | 'failed' | 'skipped' | 'canceled'
export type Trigger = 'schedule' | 'manual' | 'retry'

export interface SlaveInfo {
  master_log_file?: string
  master_log_pos?: number
  gtid_executed?: string
  raw?: string
}

export interface Job {
  id: string
  host_id: number
  project_id?: number
  project: string
  type: JobType
  chain_id?: string
  base_job_id?: string
  trigger: Trigger
  status: JobStatus
  scheduled_at: string
  started_at?: string
  finished_at?: string
  raw_size?: number
  compressed_size?: number
  from_lsn?: number
  to_lsn?: number
  slave_info?: SlaveInfo
  used_bitmap: boolean
  error_code?: string
  error_message?: string
  log_tail?: string
  path?: string
  updated_at: string
}

export interface Progress {
  compressed_bytes: number
  bytes_per_second: number
  elapsed: Duration
}

export interface Mount {
  source: string
  destination: string
}

export interface Container {
  id: string
  name: string
  image: string
  state: string
  running: boolean
  ports: string[]
  mounts: Mount[]
}

export interface DiskUsage {
  path: string
  role: 'backup' | 'data'
  total: number
  free: number
}

export interface BitmapUsage {
  project: string
  bytes: number
  checked_at: string
}

export interface AgentState {
  current?: Job
  progress?: Progress
  queue: Job[]
  paused: boolean
}

export type HostStatus = 'online' | 'offline' | 'never' | 'revoked'

export interface Host {
  id: number
  name: string
  hostname: string
  ip: string
  agent_version: string
  api_version: number
  backup_root: string
  last_heartbeat_at?: string
  metrics: { disks: DiskUsage[] | null; bitmaps: BitmapUsage[] | null }
  containers: Container[]
  state: AgentState
  config_version: number
  applied_config_version: number
  paused: boolean
  revoked_at?: string
  created_at: string
  status: HostStatus
  compatible: boolean
}

export interface RegisterToken {
  id: number
  note: string
  expires_at: string
  created_at: string
  token?: string
}

export type Tier = 'important' | 'normal'
export type IncrMode = 'chained' | 'differential'

export interface MySQLConfig {
  user: string
  password?: string
  socket?: string
  host?: string
  port?: number
}

export interface ProjectConfig {
  id?: number
  name: string
  container: string
  tier: Tier
  full_interval: Duration
  incr_interval: Duration
  offset: Duration
  incr_mode: IncrMode
  retention_days: number
  lag_threshold: Duration
  parallel: number
  throttle?: number
  timeout: Duration
  retry_count: number
  retry_interval: Duration
  priority: number
  disabled: boolean
  mysql: MySQLConfig
}

export interface Project extends ProjectConfig {
  id: number
  host_id: number
  host_name: string
  config_version: number
  applied: boolean
  password_set: boolean
  delete_policy?: 'keep' | 'purge'
  deleted_at?: string
  purged_at?: string
  created_at: string
  updated_at: string
}

export interface OffsetScore {
  offset: Duration
  overloaded_hours: number
  max_hour_load: Duration
  total_wait: Duration
}

export type CommandStatus = 'pending' | 'sent' | 'done' | 'failed' | 'expired'

export interface CommandResult {
  ok: boolean
  message?: string
  data?: unknown
}

export interface Command {
  id: number
  host_id: number
  type: string
  status: CommandStatus
  created_at: string
  expires_at: string
  acked_at?: string
  finished_at?: string
  result?: CommandResult
}

export interface Check {
  name: string
  ok: boolean
  message?: string
}

export interface JobEvent {
  seq: number
  at: string
  type: 'status' | 'progress' | 'cleanup' | 'message'
  payload: Record<string, unknown>
}

export interface Chain {
  id: string
  host_id: number
  project_id?: number
  project: string
  full_job_id?: string
  started_at: string
  last_job_at?: string
  status: 'running' | 'active' | 'failed' | 'deleted'
  deleted_at?: string
}

export interface JobDetail extends Job {
  host_name: string
  chain?: Chain
  base?: Job
  events: JobEvent[]
  log_uploaded_at?: string
}

export interface ChainView extends Chain {
  jobs: Job[]
}

export interface RestorePoint {
  job_id: string
  type: JobType
  at: string
}

export interface RestoreGuide {
  target: Job
  sequence: Job[]
  environment: RestoreEnv
  steps: { title: string; commands: string }[]
  notes: string[]
}

export interface PredictedRun {
  project: string
  type: JobType
  at: string
  start?: string
  end?: string
  superseded: boolean
  estimate: Duration
}

export interface HourLoad {
  hour: string
  total: Duration
  overloaded: boolean
}

export interface Schedule {
  host_id: number
  from: string
  to: string
  timezone: string
  runs: PredictedRun[]
  hours: HourLoad[]
  actual: Job[]
}

export interface JobStats {
  success: number
  warning: number
  failed: number
  skipped: number
  bytes: number
}

export interface ProjectHealth {
  id: number
  name: string
  host_id: number
  host_name: string
  enabled: boolean
  last_full?: string
  last_incr?: string
  stale: boolean
}

export interface DiskView extends DiskUsage {
  host_id: number
  host_name: string
  used_ratio: number
  alert: boolean
}

export interface Dashboard {
  hosts: Host[]
  today_from: string
  today: JobStats
  projects: ProjectHealth[]
  disks: DiskView[]
  recent_failures: Job[]
}

export interface AuditLog {
  id: number
  user_id?: number
  username: string
  action: string
  target_type: string
  target_id: string
  detail: Record<string, unknown>
  ip: string
  created_at: string
}

export interface RestoreEnv {
  host: string
  backup_root?: string
  container?: string
  image?: string
  data_dir?: string
  dir: string
}

export interface AssistantInfo {
  enabled: boolean
  models: string[]
}

export interface AssistantSession {
  id: number
  title: string
  created_at: string
  updated_at: string
}

export interface AssistantToolCall {
  id: string
  name: string
  arguments: string
  result: string
  error?: boolean
  // pending is set while the tool runs.
  pending?: boolean
}

export interface AssistantMessage {
  id: number
  session_id: number
  role: 'user' | 'assistant'
  content: string
  reasoning?: string
  tool_calls: AssistantToolCall[]
  model?: string
  error?: string
  input_tokens?: number
  output_tokens?: number
  created_at: string
}

export interface AssistantSessionDetail extends AssistantSession {
  messages: AssistantMessage[]
}

export interface ChatContext {
  job_id?: string
  host_id?: number
  project_id?: number
}

export interface ChatEvent {
  type: 'user_message' | 'content' | 'reasoning' | 'tool_call' | 'tool_result' | 'error' | 'done'
  content?: string
  tool_call?: { id: string; name: string; arguments: string }
  tool_result?: { id: string; name: string; content: string; error?: boolean }
  message?: AssistantMessage
  error?: string
}
