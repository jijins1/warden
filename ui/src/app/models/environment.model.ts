// Environment model
export interface Environment {
  name: string;
  description?: string;
  keyId?: string;
  createdAt: string; // ISO date
  runCount: number;
  lastRunAt?: string;
}

// Environment with key info
export interface EnvironmentDetail extends Environment {
  publicKey?: string;
  fingerprint?: string;
}
