import { afterEach, describe, expect, it, vi } from 'vitest'

import { cancelAutomaticBackupRestoreTask, exportBackup, fetchActiveAutomaticBackupRestoreTask, fetchAutomaticBackupRestoreTask, fetchBackupSections, inspectAutomaticBackupFile, inspectBackupFile, startAutomaticBackupRestore } from '@/features/settings/api/settings'
import { setCSRFToken } from '@/lib/http'

afterEach(() => {
  setCSRFToken(null)
  vi.unstubAllGlobals()
})

describe('automatic backup restore tasks', () => {
  it('starts an authenticated background restore task', async () => {
    setCSRFToken('csrf-token')
    const restore = {
      id: 'task-id',
      key: 'backups/latest.xlyra',
      filename: 'latest.xlyra',
      status: 'running',
      started_at: '2026-08-24T00:00:00Z',
      progress: { step: 'download', status: 'in_progress' },
    }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ restore }), {
      status: 202,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await startAutomaticBackupRestore('backups/latest.xlyra')

    const [, request] = fetchMock.mock.calls[0]
    const headers = new Headers(request.headers)
    expect(headers.get('X-CSRF-Token')).toBe('csrf-token')
    expect(JSON.parse(request.body)).toEqual({ key: 'backups/latest.xlyra' })
    expect(result.restore.id).toBe('task-id')
  })

  it('loads the latest restore task progress', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      restore: {
        id: 'task/id',
        status: 'completed',
        progress: { step: 'complete', status: 'complete' },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await fetchAutomaticBackupRestoreTask('task/id')

    expect(fetchMock.mock.calls[0][0]).toContain('/restore/task%2Fid')
    expect(result.restore.status).toBe('completed')
  })

  it('loads the active restore task after a page refresh', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ restore: null }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await fetchActiveAutomaticBackupRestoreTask()

    expect(fetchMock.mock.calls[0][0]).toContain('/restore/active')
    expect(result.restore).toBeNull()
  })

  it('requests cancellation for a running restore task', async () => {
    setCSRFToken('csrf-token')
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      restore: {
        id: 'task-id',
        status: 'canceling',
        cancellable: false,
        progress: { step: 'import', status: 'in_progress' },
      },
    }), { status: 202, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await cancelAutomaticBackupRestoreTask('task-id')

    const [, request] = fetchMock.mock.calls[0]
    expect(request.method).toBe('DELETE')
    expect(new Headers(request.headers).get('X-CSRF-Token')).toBe('csrf-token')
    expect(result.restore.status).toBe('canceling')
  })
})

describe('backup sections', () => {
  it('fetches the backup section catalog', async () => {
    const payload = {
      sections: [{ id: 'settings', group: 'system', requires: [], recommends: [], default_manual: true, default_auto: true, available: true }],
      defaults: { manual: ['settings'], auto: ['settings'] },
    }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(payload), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await fetchBackupSections()

    const [url] = fetchMock.mock.calls[0]
    expect(url).toContain('/api/v1/settings/backup/sections')
    expect(result.sections[0].id).toBe('settings')
  })

  it('normalizes null arrays from the server into empty arrays', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      sections: [{ id: 'settings', group: 'system', requires: null, recommends: null, default_manual: true, default_auto: true, available: true }],
      defaults: { manual: null, auto: null },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await fetchBackupSections()

    expect(result.sections[0].requires).toEqual([])
    expect(result.sections[0].recommends).toEqual([])
    expect(result.defaults).toEqual({ manual: [], auto: [] })
  })

  it('exports a backup without sections when omitted', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ download: { url: '/download', filename: 'backup.xlyra' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await exportBackup('secret')

    const [, request] = fetchMock.mock.calls[0]
    expect(request.method).toBe('POST')
    expect(JSON.parse(request.body)).toEqual({ passphrase: 'secret' })
  })

  it('exports a backup with an explicit section selection', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ download: { url: '/download', filename: 'backup.xlyra' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await exportBackup('secret', ['settings', 'gateway_sites'])

    const [, request] = fetchMock.mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({ passphrase: 'secret', sections: ['settings', 'gateway_sites'] })
  })

  it('uploads only the first 4 MiB when inspecting a backup file', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      backup: { format_version: 2, created_at: '2026-08-24T00:00:00Z', legacy: false, sections: ['settings'], impact: [], unavailable: [] },
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    const content = new Uint8Array(5 * 1024 * 1024)
    const file = new File([content], 'backup.xlyra', { type: 'application/octet-stream' })
    const result = await inspectBackupFile(file, 'secret')

    const [url, request] = fetchMock.mock.calls[0]
    expect(url).toContain('/api/v1/settings/backup/inspect')
    expect(request.method).toBe('POST')
    expect(request.body).toBeInstanceOf(FormData)

    const uploaded = request.body.get('file') as File
    expect(uploaded.name).toBe('backup.xlyra')
    expect(uploaded.size).toBe(4 * 1024 * 1024)
    expect(request.body.get('passphrase')).toBe('secret')
    expect(result.backup.sections).toEqual(['settings'])
  })

  it('inspects an automatic backup file by key', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      backup: { format_version: 2, created_at: '2026-08-24T00:00:00Z', legacy: false, sections: [], impact: [], unavailable: [] },
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await inspectAutomaticBackupFile('backups/latest.xlyra')

    const [url, request] = fetchMock.mock.calls[0]
    expect(url).toContain('/api/v1/settings/backup/automatic/files/inspect')
    expect(request.method).toBe('POST')
    expect(JSON.parse(request.body)).toEqual({ key: 'backups/latest.xlyra' })
  })
})
