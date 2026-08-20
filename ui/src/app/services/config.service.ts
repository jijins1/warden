import { Injectable, signal, computed } from '@angular/core';

@Injectable({
  providedIn: 'root'
})
export class ConfigService {
  // Signals for reactive state
  private readonly _apiUrl = signal<string>(this.getDefaultApiUrl());
  private readonly _wsUrl = signal<string>('');
  private readonly _pageSize = signal<number>(50);
  private readonly _maxPageSize = signal<number>(500);
  private readonly _pollInterval = signal<number>(5000);
  private readonly _insecureDevAuth = signal<boolean>(this.isDefaultInsecureDevAuth());
  private readonly _userSubject = signal<string | null>(this.getStoredUserSubject());

  // Public readonly signals
  readonly apiUrl = this._apiUrl.asReadonly();
  readonly wsUrl = this._wsUrl.asReadonly();
  readonly pageSize = this._pageSize.asReadonly();
  readonly maxPageSize = this._maxPageSize.asReadonly();
  readonly pollInterval = this._pollInterval.asReadonly();
  readonly insecureDevAuth = this._insecureDevAuth.asReadonly();
  readonly userSubject = this._userSubject.asReadonly();

  // Computed signals
  readonly isAuthenticated = computed(() => this._userSubject() !== null);

  constructor() {
    // Compute WebSocket URL from API URL
    this.updateWsUrl();
  }

  private getDefaultApiUrl(): string {
    const envUrl = import.meta.env['NG_APP_API_URL'];
    return envUrl || '/api';
  }

  private isDefaultInsecureDevAuth(): boolean {
    const envAuth = import.meta.env['NG_APP_INSECURE_DEV_AUTH'];
    return envAuth === 'true';
  }

  private getStoredUserSubject(): string | null {
    return localStorage.getItem('warden_user_subject');
  }

  private updateWsUrl(): void {
    const apiUrl = this._apiUrl();
    const wsUrl = apiUrl.replace(/^http/, 'ws');
    this._wsUrl.set(wsUrl);
  }

  setUserSubject(subject: string): void {
    localStorage.setItem('warden_user_subject', subject);
    this._userSubject.set(subject);
  }

  clearUserSubject(): void {
    localStorage.removeItem('warden_user_subject');
    this._userSubject.set(null);
  }
}
