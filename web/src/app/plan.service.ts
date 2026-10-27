import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';
import { catchError, Observable, throwError } from 'rxjs';
import { PlanParams, PlanResponse } from './plan';

@Injectable({ providedIn: 'root' })
export class PlanService {
  private readonly http = inject(HttpClient);

  /** Posts the GPX and params. The URL is relative so the app works under /cheersquad/. */
  plan(gpx: File, params: PlanParams): Observable<PlanResponse> {
    const body = new FormData();
    body.append('gpx', gpx);
    body.append('params', JSON.stringify(params));
    return this.http
      .post<PlanResponse>('api/plan', body)
      .pipe(
        catchError((err: HttpErrorResponse) =>
          throwError(() => new Error(err.error?.error ?? `Request failed (${err.status})`)),
        ),
      );
  }
}
