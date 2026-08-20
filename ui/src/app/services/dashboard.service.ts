import { Injectable } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable } from 'rxjs';

import { ConfigService } from './config.service';
import { RunSummary } from '../models/run.model';

@Injectable({
  providedIn: 'root'
})
export class DashboardService {
  constructor(
    private http: HttpClient,
    private config: ConfigService
  ) {}

  /**
   * Get summary statistics for the dashboard
   */
  getSummary(since?: string): Observable<RunSummary> {
    const apiUrl = this.config.getApiUrl();
    
    let params = new HttpParams();
    if (since) {
      params = params.set('since', since);
    }

    return this.http.get<RunSummary>(`${apiUrl}/admin/summary`, { params });
  }

  /**
   * Get recent runs for the dashboard
   */
  getRecentRuns(limit: number = 10): Observable<any[]> {
    const apiUrl = this.config.getApiUrl();
    
    let params = new HttpParams();
    params = params.set('limit', limit.toString());

    return this.http.get<any[]>(`${apiUrl}/runs`, { params });
  }

  /**
   * Get runs by date (for chart)
   */
  getRunsByDate(days: number = 30): Observable<Record<string, number>> {
    const apiUrl = this.config.getApiUrl();
    return this.http.get<Record<string, number>>(`${apiUrl}/admin/runs-by-date?days=${days}`);
  }

  /**
   * Get activity by hour (for chart)
   */
  getActivityByHour(): Observable<Record<string, number>> {
    const apiUrl = this.config.getApiUrl();
    return this.http.get<Record<string, number>>(`${apiUrl}/admin/activity-by-hour`);
  }
}
