import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { MatCardModule } from '@angular/material/card';
import { MatTableModule, MatTableDataSource } from '@angular/material/table';
import { MatPaginatorModule, MatPaginator, PageEvent } from '@angular/material/paginator';
import { MatSortModule } from '@angular/material/sort';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { MatSelectModule } from '@angular/material/select';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { FormsModule } from '@angular/forms';
import { RouterModule } from '@angular/router';

import { HostsService } from '../../services/hosts.service';
import { HostListItem, HostFilter } from '../../models/host.model';

@Component({
  selector: 'app-hosts-page',
  standalone: true,
  imports: [
    CommonModule,
    MatCardModule,
    MatTableModule,
    MatPaginatorModule,
    MatSortModule,
    MatFormFieldModule,
    MatInputModule,
    MatSelectModule,
    MatButtonModule,
    MatIconModule,
    MatProgressSpinnerModule,
    FormsModule,
    RouterModule
  ],
  template: `
    <div class="hosts-page">
      <h1 class="mat-h1">Machines</h1>

      <!-- Filters -->
      <mat-card class="filter-card">
        <mat-card-content>
          <form (ngSubmit)="applyFilters()" class="filter-form">
            <div class="filter-row">
              <mat-form-field appearance="outline" class="filter-field">
                <mat-label>Environnement</mat-label>
                <mat-select [(value)]="filter.environment" (selectionChange)="applyFilters()">
                  <mat-option value="">Tous</mat-option>
                  <mat-option *ngFor="let env of environments" [value]="env">{{ env }}</mat-option>
                </mat-select>
              </mat-form-field>

              <mat-form-field appearance="outline" class="filter-field">
                <mat-label>Rechercher</mat-label>
                <input matInput [(ngModel)]="filter.search" (ngModelChange)="debounceApply()" name="search">
              </mat-form-field>

              <div class="filter-actions">
                <button mat-button type="button" (click)="clearFilters()" [disabled]="!hasFilters()">
                  <mat-icon>clear</mat-icon>
                  Effacer
                </button>
              </div>
            </div>
          </form>
        </mat-card-content>
      </mat-card>

      <!-- Results -->
      <mat-card>
        <mat-card-content>
          <div *ngIf="loading && dataSource.data.length === 0" class="loading-spinner">
            <mat-spinner diameter="50"></mat-spinner>
          </div>

          <div *ngIf="!loading && dataSource.data.length === 0" class="empty-state">
            <mat-icon>dns</mat-icon>
            <p>Aucune machine trouvée</p>
          </div>

          <div *ngIf="dataSource.data.length > 0" class="table-container">
            <table mat-table [dataSource]="dataSource" class="hosts-table">
              <!-- Host Column -->
              <ng-container matColumnDef="host">
                <th mat-header-cell *matHeaderCellDef>Nom</th>
                <td mat-cell *matCellDef="let host">
                  <a [routerLink]="['/hosts', host.host]">{{ host.host }}</a>
                </td>
              </ng-container>

              <!-- Environment Column -->
              <ng-container matColumnDef="environment">
                <th mat-header-cell *matHeaderCellDef>Environnement</th>
                <td mat-cell *matCellDef="let host">{{ host.environment }}</td>
              </ng-container>

              <!-- Address Column -->
              <ng-container matColumnDef="address">
                <th mat-header-cell *matHeaderCellDef>Adresse</th>
                <td mat-cell *matCellDef="let host">{{ host.address }}</td>
              </ng-container>

              <!-- Login Column -->
              <ng-container matColumnDef="login">
                <th mat-header-cell *matHeaderCellDef>Utilisateur</th>
                <td mat-cell *matCellDef="let host">{{ host.login }}</td>
              </ng-container>

              <!-- RunCount Column -->
              <ng-container matColumnDef="runCount">
                <th mat-header-cell *matHeaderCellDef>Exécutions</th>
                <td mat-cell *matCellDef="let host">{{ host.runCount }}</td>
              </ng-container>

              <!-- LastRunAt Column -->
              <ng-container matColumnDef="lastRunAt">
                <th mat-header-cell *matHeaderCellDef>Dernière exécution</th>
                <td mat-cell *matCellDef="let host">{{ host.lastRunAt ? formatDate(host.lastRunAt) : '-' }}</td>
              </ng-container>

              <!-- EnrolledAt Column -->
              <ng-container matColumnDef="enrolledAt">
                <th mat-header-cell *matHeaderCellDef>Enrôlée le</th>
                <td mat-cell *matCellDef="let host">{{ formatDate(host.enrolledAt) }}</td>
              </ng-container>

              <tr mat-header-row *matHeaderRowDef="displayedColumns; sticky: true"></tr>
              <tr mat-row *matRowDef="let row; columns: displayedColumns"></tr>
            </table>
          </div>
        </mat-card-content>
      </mat-card>
    </div>
  `,
  styles: [`
    .hosts-page {
      max-width: 1600px;
      margin: 0 auto;
    }

    .filter-card {
      margin-bottom: 20px;
    }

    .filter-form {
      display: flex;
      flex-direction: column;
      gap: 16px;
    }

    .filter-row {
      display: flex;
      flex-wrap: wrap;
      gap: 16px;
      align-items: flex-end;
    }

    .filter-field {
      flex: 1;
      min-width: 180px;
    }

    .filter-actions {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .loading-spinner {
      display: flex;
      justify-content: center;
      align-items: center;
      height: 200px;
    }

    .empty-state {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      padding: 40px;
      color: rgba(0, 0, 0, 0.6);

      mat-icon {
        font-size: 48px;
        margin-bottom: 16px;
      }
    }

    .table-container {
      overflow-x: auto;
    }

    .hosts-table {
      width: 100%;
      min-width: 1000px;
    }

    .hosts-table th {
      background-color: #f5f5f5;
      font-weight: 500;
    }

    .hosts-table a {
      color: #1565c0;
      text-decoration: none;
      font-weight: 500;

      &:hover {
        text-decoration: underline;
      }
    }
  `]
})
export class HostsPageComponent implements OnInit {
  @ViewChild(MatPaginator) paginator!: MatPaginator;

  displayedColumns: string[] = ['host', 'environment', 'address', 'login', 'runCount', 'lastRunAt', 'enrolledAt'];
  dataSource = new MatTableDataSource<HostListItem>([]);

  filter: HostFilter = {};
  environments: string[] = [];
  loading = true;
  private debounceTimer: any;

  constructor(private hostsService: HostsService) {}

  ngOnInit(): void {
    this.loadData();
  }

  loadData(): void {
    this.loading = true;

    this.hostsService.listHosts(this.filter).subscribe({
      next: (hosts) => {
        this.dataSource.data = hosts;
        
        // Extract unique environments
        this.environments = [...new Set(hosts.map(h => h.environment).filter(Boolean))].sort();
        
        this.loading = false;
      },
      error: () => {
        this.loading = false;
      }
    });
  }

  applyFilters(): void {
    clearTimeout(this.debounceTimer);
    this.loadData();
  }

  debounceApply(): void {
    clearTimeout(this.debounceTimer);
    this.debounceTimer = setTimeout(() => this.applyFilters(), 500);
  }

  clearFilters(): void {
    this.filter = {};
    this.loadData();
  }

  hasFilters(): boolean {
    return Object.values(this.filter).some(v => v !== undefined && v !== null && v !== '');
  }

  formatDate(dateString: string): string {
    const date = new Date(dateString);
    return date.toLocaleString('fr-FR', {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    });
  }
}
