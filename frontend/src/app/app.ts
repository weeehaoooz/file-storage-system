import { Component, OnInit, signal, inject, computed } from '@angular/core';
import { HttpEventType, HttpEvent } from '@angular/common/http';
import { DatePipe } from '@angular/common';
import { Subscription } from 'rxjs';
import { FileService, FileRecord } from './file.service';

export interface UploadItem {
  id: string;
  name: string;
  size: number;
  progress: number;
  status: 'pending' | 'uploading' | 'completed' | 'failed';
  error?: string;
  subscription?: Subscription;
}

export interface VirtualItem {
  id: string; // dir:path or fileRecord.id
  name: string;
  isFolder: boolean;
  size?: number;
  type?: string;
  createdAt?: string;
  path?: string; // only for folders
  fileRecord?: FileRecord; // only for files
}

@Component({
  selector: 'app-root',
  imports: [DatePipe],
  templateUrl: './app.html',
  styleUrl: './app.css'
})
export class App implements OnInit {
  private fileService = inject(FileService);

  // States
  protected readonly files = signal<FileRecord[]>([]);
  protected readonly uploadQueue = signal<UploadItem[]>([]);
  protected readonly isDragging = signal<boolean>(false);
  
  // Navigation State
  protected readonly currentPath = signal<string>(''); // e.g. "" or "Studio Work/"
  protected readonly searchQuery = signal<string>('');

  // Inline editing states
  protected readonly editingFileId = signal<string | null>(null);
  protected readonly editingName = signal<string>('');

  // Computed: Breadcrumbs segments based on currentPath
  // e.g. "Studio Work/Branding/" -> [{name: 'Home', path: ''}, {name: 'Studio Work', path: 'Studio Work/'}, {name: 'Branding', path: 'Studio Work/Branding/'}]
  protected readonly breadcrumbs = computed(() => {
    const path = this.currentPath();
    const list = [{ name: 'Home', path: '' }];
    if (!path) return list;

    const parts = path.split('/').filter(Boolean);
    let cumulativePath = '';
    for (const part of parts) {
      cumulativePath += part + '/';
      list.push({ name: part, path: cumulativePath });
    }
    return list;
  });

  // Helper to find the parent directory path
  protected getParentPath(): string {
    const path = this.currentPath();
    if (!path) return '';
    const parts = path.split('/').filter(Boolean);
    parts.pop();
    return parts.length > 0 ? parts.join('/') + '/' : '';
  }

  // Computed: Parsed Virtual Items (Folders and Files) in the current directory
  // If searchQuery is present, we bypass the folder structure and search globally
  protected readonly explorerItems = computed<VirtualItem[]>(() => {
    const query = this.searchQuery().toLowerCase().trim();
    const allFiles = this.files().filter(f => f.status === 'active');
    
    // 1. Search Mode: global flat list of matching files
    if (query) {
      return allFiles
        .filter(f => 
          f.name.toLowerCase().includes(query) || 
          f.type.toLowerCase().includes(query) ||
          f.createdAt.toLowerCase().includes(query)
        )
        .map(f => ({
          id: f.id,
          name: f.name, // Display the full path in search mode so users know where it is
          isFolder: false,
          size: f.size,
          type: f.type,
          createdAt: f.createdAt,
          fileRecord: f
        }));
    }

    // 2. Folder Navigation Mode: Group items inside currentPath
    const prefix = this.currentPath();
    const itemsMap = new Map<string, VirtualItem>();

    for (const file of allFiles) {
      if (!file.name.startsWith(prefix)) continue;

      const remainder = file.name.substring(prefix.length);
      if (!remainder) continue; // Skip directory placeholder file itself

      const slashIndex = remainder.indexOf('/');
      if (slashIndex !== -1) {
        // It's a subfolder
        const folderName = remainder.substring(0, slashIndex);
        const folderPath = prefix + folderName + '/';
        const folderKey = 'dir:' + folderPath;

        if (!itemsMap.has(folderKey)) {
          itemsMap.set(folderKey, {
            id: folderKey,
            name: folderName,
            isFolder: true,
            path: folderPath
          });
        }
      } else {
        // It's a file
        // S3 folders can contain placeholder 0-byte files representing the folder. Skip if file is placeholder
        if (file.type === 'application/x-directory' && file.size === 0) continue;

        itemsMap.set(file.id, {
          id: file.id,
          name: remainder,
          isFolder: false,
          size: file.size,
          type: file.type,
          createdAt: file.createdAt,
          fileRecord: file
        });
      }
    }

    // Sort: folders first, then files alphabetically
    const sorted = Array.from(itemsMap.values()).sort((a, b) => {
      if (a.isFolder && !b.isFolder) return -1;
      if (!a.isFolder && b.isFolder) return 1;
      return a.name.localeCompare(b.name);
    });

    // If inside a subfolder, prepend the virtual parent row ".."
    if (prefix) {
      const parentRow: VirtualItem = {
        id: 'dir:..',
        name: '..',
        isFolder: true,
        path: this.getParentPath()
      };
      return [parentRow, ...sorted];
    }

    return sorted;
  });

  ngOnInit() {
    this.loadFiles();
  }

  protected loadFiles() {
    this.fileService.getFiles().subscribe({
      next: (data) => {
        this.files.set(data);
      },
      error: (err) => {
        console.error('Failed to load files:', err);
      }
    });
  }

  // --- Folder Navigation Handlers ---
  protected openFolder(folderPath: string) {
    this.currentPath.set(folderPath);
    this.searchQuery.set(''); // Clear search when navigating
  }

  protected navigateToBreadcrumb(path: string) {
    this.currentPath.set(path);
    this.searchQuery.set('');
  }

  protected createFolder() {
    const folderName = prompt('Enter new folder name:');
    if (!folderName) return;

    const trimmed = folderName.trim().replace(/\//g, ''); // strip any slashes
    if (!trimmed) return;

    const folderPath = this.currentPath() + trimmed + '/';

    // Register 0-byte folder placeholder in S3 backend
    this.fileService.getPresignedUrl(folderPath, 0, 'application/x-directory').subscribe({
      next: (res) => {
        // PUT empty Blob to storage to finalize registration
        const emptyFile = new File([], folderPath, { type: 'application/x-directory' });
        this.fileService.uploadToPresignedUrl(res.uploadUrl, emptyFile).subscribe({
          next: (event: HttpEvent<any>) => {
            if (event.type === HttpEventType.Response) {
              this.loadFiles();
            }
          },
          error: (err) => {
            console.error('Failed to finalize folder creation in storage:', err);
          }
        });
      },
      error: (err) => {
        console.error('Failed to pre-register folder:', err);
      }
    });
  }

  // --- Drag and Drop Handlers ---
  protected readonly hoveredFolderId = signal<string | null>(null);
  protected readonly hoveredCrumbPath = signal<string | null>(null);
  protected readonly draggedFile = signal<VirtualItem | null>(null);

  protected onBreadcrumbDragOver(event: DragEvent, crumb: { name: string, path: string }) {
    event.preventDefault();
    event.stopPropagation();
    this.hoveredCrumbPath.set(crumb.path);
  }

  protected onBreadcrumbDragLeave(event: DragEvent, crumb: { name: string, path: string }) {
    event.preventDefault();
    event.stopPropagation();
    this.hoveredCrumbPath.set(null);
  }

  protected onBreadcrumbDrop(event: DragEvent, crumb: { name: string, path: string }) {
    event.preventDefault();
    event.stopPropagation();
    this.hoveredCrumbPath.set(null);
    this.isDragging.set(false);

    // 1. Move internal file to breadcrumb path
    const internalFile = this.draggedFile();
    if (internalFile && internalFile.fileRecord) {
      const fileNameOnly = internalFile.fileRecord.name.split('/').pop() || internalFile.fileRecord.name;
      const newFullPath = crumb.path + fileNameOnly;

      this.fileService.renameFile(internalFile.id, newFullPath).subscribe({
        next: () => {
          this.loadFiles();
          this.draggedFile.set(null);
        },
        error: (err) => {
          console.error('Failed to move file to breadcrumb:', err);
          this.draggedFile.set(null);
        }
      });
      return;
    }

    // 2. Or upload external desktop files into breadcrumb path
    if (event.dataTransfer && event.dataTransfer.files) {
      const fileList = event.dataTransfer.files;
      for (let i = 0; i < fileList.length; i++) {
        const file = fileList[i];
        const s3PathName = crumb.path + file.name;
        this.startUpload(file, s3PathName);
      }
    }
  }

  protected onFileDragStart(event: DragEvent, item: VirtualItem) {
    if (item.isFolder) return;
    this.draggedFile.set(item);
    if (event.dataTransfer) {
      event.dataTransfer.effectAllowed = 'move';
      event.dataTransfer.setData('text/plain', item.id);
    }
  }

  protected onFileDragEnd(event: DragEvent, item: VirtualItem) {
    this.draggedFile.set(null);
  }

  protected onFolderDragOver(event: DragEvent, folder: VirtualItem) {
    event.preventDefault();
    event.stopPropagation();
    this.hoveredFolderId.set(folder.id);
  }

  protected onFolderDragLeave(event: DragEvent, folder: VirtualItem) {
    event.preventDefault();
    event.stopPropagation();
    this.hoveredFolderId.set(null);
  }

  protected onFolderDrop(event: DragEvent, folder: VirtualItem) {
    event.preventDefault();
    event.stopPropagation();
    this.hoveredFolderId.set(null);
    this.isDragging.set(false);

    // 1. Check if moving an internal file
    const internalFile = this.draggedFile();
    if (internalFile && internalFile.fileRecord) {
      const fileNameOnly = internalFile.fileRecord.name.split('/').pop() || internalFile.fileRecord.name;
      const newFullPath = folder.path + fileNameOnly;

      this.fileService.renameFile(internalFile.id, newFullPath).subscribe({
        next: () => {
          this.loadFiles();
          this.draggedFile.set(null);
        },
        error: (err) => {
          console.error('Failed to move file:', err);
          this.draggedFile.set(null);
        }
      });
      return;
    }

    // 2. Otherwise, upload external files directly into the folder prefix path
    if (event.dataTransfer && event.dataTransfer.files) {
      const fileList = event.dataTransfer.files;
      for (let i = 0; i < fileList.length; i++) {
        const file = fileList[i];
        const s3PathName = folder.path + file.name;
        this.startUpload(file, s3PathName);
      }
    }
  }

  protected onDragOver(event: DragEvent) {
    event.preventDefault();
    event.stopPropagation();
    
    // Only show the upload overlay if external desktop files are being dragged
    const isFileDrag = event.dataTransfer?.types.includes('Files');
    if (isFileDrag) {
      this.isDragging.set(true);
    }
  }

  protected onDragLeave(event: DragEvent) {
    event.preventDefault();
    event.stopPropagation();
    this.isDragging.set(false);
  }

  protected onDrop(event: DragEvent) {
    event.preventDefault();
    event.stopPropagation();
    this.isDragging.set(false);

    const dt = event.dataTransfer;
    if (dt && dt.types.includes('Files') && dt.files) {
      this.handleFiles(dt.files);
    }
  }

  protected onFileSelected(event: Event) {
    const element = event.currentTarget as HTMLInputElement;
    if (element.files) {
      this.handleFiles(element.files);
    }
  }

  private handleFiles(fileList: FileList) {
    for (let i = 0; i < fileList.length; i++) {
      const file = fileList[i];
      this.startUpload(file);
    }
  }

  // --- Upload Pipeline (S3 flow) ---
  private startUpload(file: File, customPath?: string) {
    const tempId = Math.random().toString(36).substring(2, 9);
    // Combine current folder path prefix with file name (S3 style)
    const s3PathName = customPath || (this.currentPath() + file.name);
    
    const queueItem: UploadItem = {
      id: tempId,
      name: file.name,
      size: file.size,
      progress: 0,
      status: 'pending'
    };

    // Add to queue
    this.uploadQueue.update(q => [queueItem, ...q]);

    // 1. Request presigned URL from API Server
    this.fileService.getPresignedUrl(s3PathName, file.size, file.type).subscribe({
      next: (res) => {
        this.updateQueueItem(tempId, { id: res.fileId, status: 'uploading' });
        const finalId = res.fileId;

        // 2. Direct PUT upload to Storage Server
        const sub = this.fileService.uploadToPresignedUrl(res.uploadUrl, file).subscribe({
          next: (event: HttpEvent<any>) => {
            if (event.type === HttpEventType.UploadProgress) {
              const progress = event.total ? Math.round((100 * event.loaded) / event.total) : 0;
              this.updateQueueItem(finalId, { progress });
            } else if (event.type === HttpEventType.Response) {
              this.updateQueueItem(finalId, { progress: 100, status: 'completed' });
              this.loadFiles();
              setTimeout(() => this.removeFromQueue(finalId), 4000);
            }
          },
          error: (err) => {
            console.error('Upload failed for file:', file.name, err);
            this.updateQueueItem(finalId, { status: 'failed', error: 'Upload failed' });
          }
        });

        this.updateQueueItem(finalId, { subscription: sub });
      },
      error: (err) => {
        console.error('Failed to get presigned URL:', err);
        this.updateQueueItem(tempId, { status: 'failed', error: 'Pre-signing failed' });
      }
    });
  }

  private updateQueueItem(id: string, updates: Partial<UploadItem>) {
    this.uploadQueue.update(q => q.map(item => {
      if (item.id === id) {
        return { ...item, ...updates };
      }
      return item;
    }));
  }

  protected cancelUpload(item: UploadItem) {
    if (item.subscription) {
      item.subscription.unsubscribe();
    }
    if (item.status === 'uploading' && item.id) {
      this.fileService.deleteFile(item.id).subscribe();
    }
    this.removeFromQueue(item.id);
  }

  private removeFromQueue(id: string) {
    this.uploadQueue.update(q => q.filter(item => item.id !== id));
  }

  // --- Rename Actions ---
  protected startRename(item: VirtualItem) {
    if (item.isFolder || !item.fileRecord) return;
    this.editingFileId.set(item.id);
    this.editingName.set(item.name);
  }

  protected cancelRename() {
    this.editingFileId.set(null);
    this.editingName.set('');
  }

  protected saveRename(item: VirtualItem, newName: string) {
    newName = newName.trim();
    if (!newName || newName === item.name || !item.fileRecord) {
      this.cancelRename();
      return;
    }

    // Build new full path name preserving parent folder prefix
    const newFullPath = this.currentPath() + newName;

    this.fileService.renameFile(item.id, newFullPath).subscribe({
      next: () => {
        this.cancelRename();
        this.loadFiles();
      },
      error: (err) => {
        console.error('Failed to rename file:', err);
        this.cancelRename();
      }
    });
  }

  // --- Delete Actions ---
  protected deleteFile(item: VirtualItem) {
    const msg = item.isFolder 
      ? `Are you sure you want to delete the folder "${item.name}" and all its contents?` 
      : `Are you sure you want to delete "${item.name}"?`;

    if (confirm(msg)) {
      if (item.isFolder) {
        // Find all file records in the DB starting with this folder's prefix path
        const prefix = item.path!;
        const filesToDelete = this.files().filter(f => f.name.startsWith(prefix));

        if (filesToDelete.length === 0) {
          this.loadFiles();
          return;
        }

        // Delete all matches in parallel, then refresh the list
        let completed = 0;
        for (const file of filesToDelete) {
          this.fileService.deleteFile(file.id).subscribe({
            next: () => {
              completed++;
              if (completed === filesToDelete.length) {
                this.loadFiles();
              }
            },
            error: (err) => {
              console.error('Failed to delete nested file:', file.name, err);
              completed++;
              if (completed === filesToDelete.length) {
                this.loadFiles();
              }
            }
          });
        }
      } else {
        // Standard file deletion
        this.fileService.deleteFile(item.id).subscribe({
          next: () => {
            this.loadFiles();
          },
          error: (err) => {
            console.error('Failed to delete file:', err);
          }
        });
      }
    }
  }

  // --- Search Handler ---
  protected onSearchInput(event: Event) {
    const input = event.target as HTMLInputElement;
    this.searchQuery.set(input.value);
  }

  // --- Utility Helpers ---
  protected formatBytes(bytes: number, decimals = 1): string {
    if (bytes === 0) return '0 Bytes';
    const k = 1024;
    const dm = decimals < 0 ? 0 : decimals;
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
  }

  protected getFileIcon(type: string, name: string = ''): string {
    const ext = name.split('.').pop()?.toLowerCase();
    if (type.startsWith('image/') || ext === 'png' || ext === 'jpg' || ext === 'jpeg' || ext === 'gif') return 'image';
    if (type.startsWith('video/')) return 'video_file';
    if (type.startsWith('audio/')) return 'audiotrack';
    if (type.includes('pdf')) return 'picture_as_pdf';
    if (type.includes('zip') || type.includes('tar') || type.includes('rar') || type.includes('compressed')) return 'zip_box';
    if (type.includes('code') || ext === 'html' || ext === 'js' || ext === 'ts' || ext === 'go' || ext === 'css' || ext === 'json') return 'terminal';
    return 'description';
  }

  protected getDownloadUrl(id: string): string {
    return `/storage/download/${id}`;
  }
}
