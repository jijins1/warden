import { Injectable, signal } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable } from 'rxjs';

import { ConfigService } from './config.service';
import { Host, HostListItem, HostFilter } from '../models/host.model';

@Injectable({
  providedIn: 'root'
})
export class HostsService {
  // Signals for state
  private readonly _hosts = signal<HostListItem[]>([]);
  private readonly _loading = signal<boolean>(false);
  private readonly _error = signal<string | null>(null);
  private readonly _filter = signal<HostFilter>({});
  private readonly _environments = signal<string[]>([]);

  // Public readonly signals
  readonly hosts = this._hosts.asReadonly();
  readonly loading = this._loading.asReadonly();
  readonly error = this._error.asReadonly();
  readonly filter = this._filter.asReadonly();
  readonly environments = this._environments.asReadonly();

  // Computed
  readonly hasHosts = computed(() => this._hosts().length > 0);

  constructor(
    private http: HttpClient,
    private config: ConfigService
  ) {}

  /**
   * List all hosts
   */
  listHosts(filter: HostFilter = {}): Observable<HostListItem[]> {
    this._loading.set(true);
    this._error.set(null);
    this._filter.set(filter);

    const apiUrl = this.config.apiUrl();
    
    let params = new HttpParams();
    if (filter.environment) {
      params = params.set('environment', filter.environment);
    }
    if (filter.search) {
      params = params.set('search', filter.search);
    }

    const observable = this.http.get<HostListItem[]>(`${apiUrl}/admin/hosts`, { params });
    
    observable.subscribe({
      next: (hosts) => {
        this._hosts.set(hosts);
        this._environments.set([...new Set(hosts.map(h => h.environment).filter(Boolean))].sort());
        this._loading.set(false);
      },
      error: (err) => {
        this._error.set(err.message || 'Failed to load hosts');
        this._loading.set(false);
      }
    });

    return observable;
  }

  /**
   * Get a specific host
   */
  getHost(environment: string, host: string): Observable<Host> {
    const apiUrl = this.config.apiUrl();
    return this.http.get<Host>(`${apiUrl}/admin/hosts/${environment}/${host}`);
  }

  /**
   * Get runs for a specific host
   */
  getHostRuns(host: string, since?: string, until?: string): Observable<any[]> {
    const apiUrl = this.config.apiUrl();
    
    let params = new HttpParams();
    if (since) {
      params = params.set('since', since);
    }
    if (until) {
      params = params.set('until', until);
    }

    return this.http.get<any[]>(`${apiUrl}/hosts/${host}/runs`, { params });
  }

  /**
   * Declare new hosts
   */
  declareHosts(hosts: Host[]): Observable<Host[]> {
    const apiUrl = this.config.apiUrl();
    return this.http.put<Host[]>(`${apiUrl}/admin/hosts`, hosts);
  }

  /**
   * Remove a host
   */
  removeHost(environment: string, host: string): Observable<void> {
    const apiUrl = this.config.apiUrl();
    return this.http.delete<void>(`${apiUrl}/admin/hosts/${environment}/${host}`);
  }

  /**
   * Update filter
   */
  updateFilter(filter: Partial<HostFilter>): void {
    this._filter.update(f => ({ ...f, ...filter }));
  }

  /**
   * Clear all filters
   */
  clearFilters(): void {
    this._filter.set({});
  }

  /**
   * Apply filters (reload data)
   */
  applyFilters(): void {
    this.listHosts(this._filter());
  }
}

// Helper for computed
import { computed } from '@angular/core';
