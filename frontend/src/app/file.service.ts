import { Injectable, inject } from '@angular/core';
import { HttpClient, HttpEvent, HttpHeaders, HttpRequest } from '@angular/common/http';
import { Observable } from 'rxjs';

export interface FileRecord {
  id: string;
  name: string;
  size: number;
  type: string;
  status: string; // 'pending' | 'active'
  createdAt: string;
}

@Injectable({
  providedIn: 'root',
})
export class FileService {
  private http = inject(HttpClient);
  private apiBaseUrl = '';

  getFiles(): Observable<FileRecord[]> {
    return this.http.get<FileRecord[]>(`${this.apiBaseUrl}/api/files`);
  }

  getPresignedUrl(name: string, size: number, type: string): Observable<{ uploadUrl: string; fileId: string }> {
    return this.http.post<{ uploadUrl: string; fileId: string }>(
      `${this.apiBaseUrl}/api/files/presign`,
      { name, size, type }
    );
  }

  uploadToPresignedUrl(uploadUrl: string, file: File): Observable<HttpEvent<any>> {
    // S3-style PUT upload requires sending the raw file in the body
    const req = new HttpRequest('PUT', uploadUrl, file, {
      headers: new HttpHeaders({
        'Content-Type': file.type || 'application/octet-stream',
      }),
      reportProgress: true,
      responseType: 'json',
    });
    return this.http.request(req);
  }

  renameFile(id: string, name: string): Observable<FileRecord> {
    return this.http.post<FileRecord>(`${this.apiBaseUrl}/api/files/rename`, { id, name });
  }

  deleteFile(id: string): Observable<{ success: boolean }> {
    return this.http.delete<{ success: boolean }>(`${this.apiBaseUrl}/api/files/${id}`);
  }
}
