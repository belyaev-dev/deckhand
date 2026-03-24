export type ClusterHealth = 'healthy' | 'warning' | 'critical' | 'unknown';
export type WebSocketStatus = 'connecting' | 'connected' | 'reconnecting' | 'disconnected' | 'error';

export interface ClusterSummary {
  namespace: string;
  name: string;
  createdAt?: string | null;
  phase?: string;
  phaseReason?: string;
  desiredInstances: number;
  readyInstances: number;
  currentPrimary?: string;
  image?: string;
  firstRecoverabilityPoint?: string | null;
  lastSuccessfulBackup?: string | null;
}

export interface ClusterNamespaceSummary {
  name: string;
  clusterCount: number;
}

export interface ClusterOverviewSummary extends ClusterSummary {
  overallHealth: ClusterHealth | string;
  metricsScrapedAt?: string | null;
  metricsScrapeError: string;
}

export interface ClusterListResponse {
  namespaces: ClusterNamespaceSummary[];
  items: ClusterOverviewSummary[];
}

export interface BackupSummary {
  namespace: string;
  name: string;
  clusterName: string;
  createdAt?: string | null;
  phase?: string;
  method?: string;
  target?: string;
  startedAt?: string | null;
  stoppedAt?: string | null;
  error?: string;
}

export interface ScheduledBackupSummary {
  namespace: string;
  name: string;
  clusterName: string;
  createdAt?: string | null;
  schedule: string;
  method?: string;
  target?: string;
  immediate: boolean;
  suspended: boolean;
  lastScheduleTime?: string | null;
  nextScheduleTime?: string | null;
}

export interface ClusterDetailResponse {
  cluster: ClusterSummary;
  backups: BackupSummary[];
  scheduledBackups: ScheduledBackupSummary[];
}

export interface ClusterBackupsResponse {
  cluster: ClusterSummary;
  backups: BackupSummary[];
  scheduledBackups: ScheduledBackupSummary[];
}

export interface CreateBackupRequest {
  method?: string;
  target?: string;
}

export interface CreateBackupResponse {
  backup: BackupSummary;
}

export interface RestoreRecoverabilityWindow {
  start?: string | null;
  end?: string | null;
}

export type RestorePhase = 'bootstrapping' | 'recovering' | 'ready' | 'failed';

export interface ClusterRestoreOptionsResponse {
  cluster: ClusterSummary;
  backups: BackupSummary[];
  recoverability: RestoreRecoverabilityWindow;
  supportedPhases: RestorePhase[] | string[];
}

export interface CreateRestoreRequest {
  backupName: string;
  targetNamespace: string;
  targetName: string;
  pitrTargetTime?: string;
}

export interface RestorePhaseTimestamps {
  bootstrappingStartedAt?: string | null;
  recoveringStartedAt?: string | null;
  readyAt?: string | null;
  failedAt?: string | null;
  lastTransitionAt?: string | null;
}

export interface RestoreStatus {
  phase: RestorePhase | string;
  phaseReason?: string;
  message?: string;
  error?: string;
  timestamps: RestorePhaseTimestamps;
}

export interface CreateRestoreResponse {
  sourceCluster: ClusterSummary;
  targetCluster: ClusterSummary;
  backup: BackupSummary;
  yamlPreview: string;
  restoreStatus: RestoreStatus;
}

export interface RestoreStatusResponse {
  cluster: ClusterSummary;
  status: RestoreStatus;
}

export interface ConnectionMetricsSummary {
  active: number;
  idle: number;
  idleInTransaction: number;
  total: number;
  maxConnections: number;
}

export interface ReplicationMetricsSummary {
  replicationLagSeconds: number;
  isReplica: boolean;
  isWalReceiverUp: boolean;
  streamingReplicas: number;
  replayLagBytes: number;
}

export interface DiskMetricsSummary {
  pvcCapacityBytes: number;
  databaseSizeBytes: number;
}

export interface InstanceMetricsSummary {
  podName: string;
  podStatus?: string;
  health: ClusterHealth | string;
  connections: ConnectionMetricsSummary;
  replication: ReplicationMetricsSummary;
  disk: DiskMetricsSummary;
  scrapedAt?: string | null;
  scrapeError?: string;
}

export interface ClusterMetricsResponse {
  cluster: ClusterSummary;
  overallHealth: ClusterHealth | string;
  scrapedAt?: string | null;
  scrapeError?: string;
  instances: InstanceMetricsSummary[];
}

export interface ErrorResponse {
  error?: string;
}

export interface WSChangeEvent {
  type: string;
  kind: string;
  action: string;
  namespace: string;
  name: string;
  occurredAt: string;
}
