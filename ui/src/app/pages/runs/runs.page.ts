import { Component, OnInit, ViewChild, signal, computed, effect } from '@angular/core';
import { CommonModule } from '@angular/common';
import { MatCardModule } from '@angular/material/card';
import { MatTableModule, MatTableDataSource } from '@angular/material/table';
import { MatPaginatorModule, MatPaginator, PageEvent } from '@angular/material/paginator';
import { MatSortModule, MatSort } from '@angular/material/sort';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { MatSelectModule } from '@angular/material/select';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatChipsModule } from '@angular/material/chips';
import { MatDatepickerModule } from '@angular/material/datepicker';
import { MatNativeDateModule } from '@angular/material/core';
import { FormsModule, ReactiveFormsModule } from '@angular/forms';
import { RouterModule } from '@angular/router';
import { RunListItem, RunFilter, RunState } from '../../models/run.model';
import { RunsService } from '../../services/runs.service';

@Component({
  selector: 'app-runs-page',
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
    MatChipsModule,
    MatDatepickerModule,
    MatNativeDateModule,
    FormsModule,
    ReactiveFormsModule,
    RouterModule
  ],
  template: `
    <div class="runs-page">
      <h1 class="mat-h1">Exécutions</h1>

      <!-- Filters -->
      <mat-card class="filter-card">
        <mat-card-content>
          <form (ngSubmit)="applyFilters()" class="filter-form">
            <div class="filter-row">
              <mat-form-field appearance="outline" class="filter-field">
                <mat-label>Environnement</mat-label>
                <mat-select [(value)]="filter().environment" (selectionChange)="onFilterChange('environment', $event.value)">
                  <mat-option value="">Tous</mat-option>
                  <mat-option *ngFor="let env of runsService.environments()" [value]="env">{{ env }}</mat-option>
                </mat-select>
              </mat-form-field>

              <mat-form-field appearance="outline" class="filter-field">
                <mat-label>Inventaire</mat-label>
                <input matInput [(ngModel)]="inventoryFilter" (ngModelChange)="debounceApply()" name="inventory">
              </mat-form-field>

              <mat-form-field appearance="outline" class="filter-field">
                <mat-label>État</mat-label>
                <mat-select [(value)]="filter().state" (selectionChange)="onFilterChange('state', $event.value)" name="state">
                  <mat-option value="">Tous</mat-option>
                  <mat-option value="pending">En attente</mat-option>
                  <mat-option value="running">En cours</mat-option>
                  <mat-option value="succeeded">Terminé</mat-option>
                  <mat-option value="failed">Échoué</mat-option>
                  <mat-option value="cancelled">Annulé</mat-option>
                </mat-select>
              </mat-form-field>

              <mat-form-field appearance="outline" class="filter-field">
                <mat-label>Changements min.</mat-label>
                <input matInput type="number" [(ngModel)]="changedMinFilter" (ngModelChange)="debounceApply()" name="changedMin">
              </mat-form-field>
            </div>

            <div class="filter-row">
              <mat-form-field appearance="outline" class="filter-field">
                <mat-label>Depuis</mat-label>
                <input matInput [matDatepicker]="startPicker" [(ngModel)]="sinceFilter" (ngModelChange)="debounceApply()" name="since">
                <mat-datepicker-toggle matSuffix [for]="startPicker"></mat-datepicker-toggle>
                <mat-datepicker #startPicker></mat-datepicker>
              </mat-form-field>

              <mat-form-field appearance="outline" class="filter-field">
                <mat-label>Jusqu'à</mat-label>
                <input matInput [matDatepicker]="endPicker" [(ngModel)]="untilFilter" (ngModelChange)="debounceApply()" name="until">
                <mat-datepicker-toggle matSuffix [for]="endPicker"></mat-datepicker-toggle>
                <mat-datepicker #endPicker></mat-datepicker>
              </mat-form-field>

              <div class="filter-actions">
                <button mat-button type="button" (click)="clearFilters()" [disabled]="!runsService.hasFilters()">
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
          <div *ngIf="runsService.loading() && runsService.runs().length === 0" class="loading-spinner">
            <mat-spinner diameter="50"></mat-spinner>
          </div>

          <div *ngIf="!runsService.loading() && runsService.runs().length === 0" class="empty-state">
            <mat-icon>inbox</mat-icon>
            <p>Aucune exécution trouvée</p>
          </div>

          <div *ngIf="runsService.runs().length > 0" class="table-container">
            <table mat-table [dataSource]="dataSource" matSort (matSortChange)="sortData($event)" class="runs-table">
              <!-- RunRef Column -->
              <ng-container matColumnDef="runRef">
                <th mat-header-cell *matHeaderCellDef mat-sort-header>Référence</th>
                <td mat-cell *matCellDef="let run">
                  <a [routerLink]="['/runs', run.runRef]">{{ run.runRef }}</a>
                </td>
              </ng-container>

              <!-- Environment Column -->
              <ng-container matColumnDef="environment">
                <th mat-header-cell *matHeaderCellDef mat-sort-header>Environnement</th>
                <td mat-cell *matCellDef="let run">{{ run.environment }}</td>
              </ng-container>

              <!-- Inventory Column -->
              <ng-container matColumnDef="inventory">
                <th mat-header-cell *matHeaderCellDef mat-sort-header>Inventaire</th>
                <td mat-cell *matCellDef="let run">{{ run.inventory }}</td>
              </ng-container>

              <!-- Playbook Column -->
              <ng-container matColumnDef="playbook">
                <th mat-header-cell *matHeaderCellDef mat-sort-header>Playbook</th>
                <td mat-cell *matCellDef="let run" class="truncate">{{ run.playbook }}</td>
              </ng-container>

              <!-- State Column -->
              <ng-container matColumnDef="state">
                <th mat-header-cell *matHeaderCellDef mat-sort-header>État</th>
                <td mat-cell *matCellDef="let run">
                  <span [class]="'status-badge ' + run.state">{{ run.state }}</span>
                </td>
              </ng-container>

              <!-- ChangedCount Column -->
              <ng-container matColumnDef="changedCount">
                <th mat-header-cell *matHeaderCellDef mat-sort-header>Changements</th>
                <td mat-cell *matCellDef="let run">{{ run.changedCount }}</td>
              </ng-container>

              <!-- StartedAt Column -->
              <ng-container matColumnDef="startedAt">
                <th mat-header-cell *matHeaderCellDef mat-sort-header>Début</th>
                <td mat-cell *matCellDef="let run">{{ formatDate(run.startedAt) }}</td>
              </ng-container>

              <!-- EndedAt Column -->
              <ng-container matColumnDef="endedAt">
                <th mat-header-cell *matHeaderCellDef mat-sort-header>Fin</th>
                <td mat-cell *matCellDef="let run">{{ run.endedAt ? formatDate(run.endedAt) : '-' }}</td>
              </ng-container>

              <!-- SecretsUnmasked Column -->
              <ng-container matColumnDef="secretsUnmasked">
                <th mat-header-cell *matHeaderCellDef mat-sort-header>Secrets</th>
                <td mat-cell *matCellDef="let run">
                  <mat-icon *ngIf="run.secretsUnmasked" color="warn" matTooltip="Les secrets sont visibles">warning</mat-icon>
                  <mat-icon *ngIf="!run.secretsUnmasked" color="primary">check</mat-icon>
                </td>
              </ng-container>

              <tr mat-header-row *matHeaderRowDef="displayedColumns; sticky: true"></tr>
              <tr mat-row *matRowDef="let row; columns: displayedColumns"></tr>
            </table>

            <mat-paginator 
              [length]="runsService.totalCount()"
              [pageSize]="filter().limit || 50"
              [pageSizeOptions]="[25, 50, 100]"
              (page)="onPageChange($event)"
              class="paginator">
            </mat-paginator>
          </div>
        </mat-card-content>
      </mat-card>
    </div>
  `,
  styles: [`
    .runs-page {
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

    .runs-table {
      width: 100%;
      min-width: 1000px;
    }

    .runs-table th {
      background-color: #f5f5f5;
      font-weight: 500;
    }

    .runs-table td {
      white-space: nowrap;
    }

    .runs-table a {
      color: #1565c0;
      text-decoration: none;
      font-weight: 500;

      &:hover {
        text-decoration: underline;
      }
    }

    .status-badge {
      padding: 4px 12px;
      border-radius: 12px;
      font-size: 0.75rem;
      font-weight: 500;
      text-transform: uppercase;

      &.pending, &.running {
        background-color: #fff3e0;
        color: #f57c00;
      }
      &.succeeded {
        background-color: #e8f5e9;
        color: #2e7d32;
      }
      &.failed {
        background-color: #ffebee;
        color: #c62828;
      }
      &.cancelled {
        background-color: #fff3e0;
        color: #f57c00;
      }
    }

    .paginator {
      margin-top: 16px;
    }
  `]
})
export class RunsPageComponent implements OnInit {
  @ViewChild(MatPaginator) paginator!: MatPaginator;
  @ViewChild(MatSort) sort!: MatSort;

  displayedColumns: string[] = ['runRef', 'environment', 'inventory', 'playbook', 'state', 'changedCount', 'startedAt', 'endedAt', 'secretsUnmasked'];
  dataSource = new MatTableDataSource<RunListItem>([]);
  
  // Local filter state for form binding
  inventoryFilter = '';
  changedMinFilter: number | undefined;
  sinceFilter = '';
  untilFilter = '';
  
  private debounceTimer: any;

  constructor(public runsService: RunsService) {
    // Effect to update dataSource when runs change
    effect(() => {
      this.dataSource.data = this.runsService.runs();
    });
  }

  ngOnInit(): void {
    this.runsService.listRuns();
  }

  filter(): RunFilter {
    const baseFilter = this.runsService.filter();
    return {
      ...baseFilter,
      inventory: this.inventoryFilter || baseFilter.inventory,
      changedMin: this.changedMinFilter || baseFilter.changedMin,
      since: this.sinceFilter || baseFilter.since,
      until: this.untilFilter || baseFilter.until
    };
  }

  onFilterChange(key: keyof RunFilter, value: any): void {
    this.runsService.updateFilter({ [key]: value } as Partial<RunFilter>);
    this.applyFilters();
  }

  applyFilters(): void {
    clearTimeout(this.debounceTimer);
    this.runsService.applyFilters();
  }

  debounceApply(): void {
    clearTimeout(this.debounceTimer);
    this.debounceTimer = setTimeout(() => this.applyFilters(), 500);
  }

  clearFilters(): void {
    this.inventoryFilter = '';
    this.changedMinFilter = undefined;
    this.sinceFilter = '';
    this.untilFilter = '';
    this.runsService.clearFilters();
  }

  onPageChange(event: PageEvent): void {
    this.runsService.updateFilter({
      limit: event.pageSize,
      cursor: event.previousPageIndex! < event.pageIndex ? 
        this.dataSource.data[this.dataSource.data.length - 1]?.runRef : undefined
    });
    this.runsService.applyFilters();
  }

  sortData(event: any): void {
    // Implement sorting
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
