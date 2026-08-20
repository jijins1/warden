// Host model for inventory
export interface Host {
  environment: string;
  host: string;
  address: string;
  login: string;
  hostKey?: string;
  enrolledAt: string; // ISO date
  lastSeenAt?: string; // ISO date
  lastRunRef?: string;
}

// Host list item
export interface HostListItem {
  environment: string;
  host: string;
  address: string;
  login: string;
  enrolledAt: string;
  runCount: number;
  lastRunAt?: string;
}

// Host filter
export interface HostFilter {
  environment?: string;
  search?: string;
}
