import type { ClusterHealth } from '../types/api';

interface StatusBadgeProps {
  status: ClusterHealth | string;
}

export function normalizeHealth(status: string): ClusterHealth {
  switch (status) {
    case 'healthy':
    case 'warning':
    case 'critical':
      return status;
    default:
      return 'unknown';
  }
}

function labelForHealth(status: ClusterHealth) {
  switch (status) {
    case 'healthy':
      return 'Healthy';
    case 'warning':
      return 'Warning';
    case 'critical':
      return 'Critical';
    default:
      return 'Unknown';
  }
}

export default function StatusBadge({ status }: StatusBadgeProps) {
  const normalizedStatus = normalizeHealth(status);

  return (
    <span className={`status-badge status-badge--${normalizedStatus}`}>
      {labelForHealth(normalizedStatus)}
    </span>
  );
}
