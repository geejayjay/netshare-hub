import { CheckCircle2, Download, FileUp, ShieldCheck, Upload } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { errorMessage } from '@/shared/api'
import { FormField } from '@/shared/components'
import { formatDateTime } from '@/shared/lib/format'
import { Alert, Badge, Button, Card, CardContent, CardHeader, CardTitle, InfoTip, Input, Switch } from '@/shared/ui'
import { downloadEncryptedBackup, useRestoreBackup, type BackupSummary } from '../api'

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const result = String(reader.result ?? '')
      const comma = result.indexOf(',')
      resolve(comma >= 0 ? result.slice(comma + 1) : result)
    }
    reader.onerror = () => reject(new Error('Could not read backup file'))
    reader.readAsDataURL(file)
  })
}

export function BackupPanel() {
  const restore = useRestoreBackup()

  // Export state
  const [exportPass, setExportPass] = useState('')
  const [smbPasswords, setSmbPasswords] = useState(true)
  const [exporting, setExporting] = useState(false)
  const [exportError, setExportError] = useState<string | null>(null)

  // Restore state
  const [restoreFile, setRestoreFile] = useState<File | null>(null)
  const [restorePass, setRestorePass] = useState('')
  const [createMissingFolders, setCreateMissingFolders] = useState(false)
  const [preview, setPreview] = useState<BackupSummary | null>(null)
  const [restoreError, setRestoreError] = useState<unknown>(null)

  async function handleExport(e: FormEvent) {
    e.preventDefault()
    setExportError(null)
    setExporting(true)
    try {
      const filename = await downloadEncryptedBackup(exportPass, smbPasswords)
      notify.success('Backup downloaded', filename)
      setExportPass('')
    } catch (err) {
      setExportError((err as Error).message || 'Backup export failed')
    } finally {
      setExporting(false)
    }
  }

  async function runRestore(dryRun: boolean) {
    if (!restoreFile) return
    if (!dryRun && !window.confirm('Replace all current users, shares, and settings with this backup?')) return
    setRestoreError(null)
    try {
      const backup = await fileToBase64(restoreFile)
      const sum = await restore.mutateAsync({
        backup,
        passphrase: restorePass,
        dryRun,
        createMissingFolders,
      })
      setPreview(sum)
      if (sum.applied) {
        notify.success('Backup restored', sum.signInAgain ? 'Please sign in again.' : 'Configuration applied.')
      } else {
        notify.info('Backup verified', `${sum.users} users, ${sum.shares} shares ready to restore.`)
      }
    } catch (err) {
      setRestoreError(err)
    }
  }

  return (
    <div className="grid gap-5 lg:grid-cols-2">
      {/* Export card */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-1.5">
            <Download className="size-4 text-primary" />
            Export Encrypted Backup
            <InfoTip content="Downloads an AES-encrypted archive containing users, shares, Samba settings, and security policies. Keep the passphrase safe — it cannot be recovered." />
          </CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={(e) => void handleExport(e)} className="flex flex-col gap-4">
            {exportError && <Alert tone="danger" title={exportError} />}

            <FormField
              label="Encryption passphrase"
              htmlFor="backup-export-pass"
              info="Minimum 12 characters. Required to decrypt and restore this backup later."
            >
              <Input
                id="backup-export-pass"
                type="password"
                minLength={12}
                maxLength={1024}
                placeholder="At least 12 characters"
                value={exportPass}
                onChange={(e) => setExportPass(e.target.value)}
                required
              />
            </FormField>

            <div className="flex items-center justify-between gap-3 rounded-lg border border-border bg-bg px-3.5 py-2.5">
              <div className="flex items-center gap-1.5">
                <span className="text-sm font-medium text-fg">Include SMB passwords</span>
                <InfoTip content="Preserves Samba NT password hashes so users do not need new SMB passwords after restoring." />
              </div>
              <Switch checked={smbPasswords} onChange={setSmbPasswords} aria-label="Include SMB passwords" />
            </div>

            <div className="flex justify-end pt-1">
              <Button type="submit" loading={exporting} disabled={exportPass.length < 12}>
                <Download className="size-4" />
                Download backup
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      {/* Restore card */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-1.5">
            <Upload className="size-4 text-primary" />
            Verify & Restore
            <InfoTip content="Always run a Dry Run first to inspect what the backup contains and check for missing share folders before replacing live configuration." />
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {restoreError !== null && <Alert tone="danger" title={errorMessage(restoreError)} />}

          <FormField label="Backup file" htmlFor="backup-restore-file">
            <label
              htmlFor="backup-restore-file"
              className="flex cursor-pointer items-center justify-between gap-3 rounded-lg border border-dashed border-border-strong bg-bg px-3.5 py-2.5 text-sm transition-colors hover:border-primary"
            >
              <span className="flex min-w-0 items-center gap-2 truncate text-fg-muted">
                <FileUp className="size-4 shrink-0 text-primary" />
                <span className="truncate">{restoreFile ? restoreFile.name : 'Choose .nshbackup file…'}</span>
              </span>
              <span className="shrink-0 text-xs font-medium text-primary">Browse</span>
              <input
                id="backup-restore-file"
                type="file"
                className="hidden"
                onChange={(e) => {
                  setRestoreFile(e.target.files?.[0] ?? null)
                  setPreview(null)
                }}
              />
            </label>
          </FormField>

          <FormField label="Passphrase" htmlFor="backup-restore-pass">
            <Input
              id="backup-restore-pass"
              type="password"
              minLength={12}
              maxLength={1024}
              placeholder="Passphrase used during export"
              value={restorePass}
              onChange={(e) => setRestorePass(e.target.value)}
            />
          </FormField>

          <div className="flex items-center justify-between gap-3 rounded-lg border border-border bg-bg px-3.5 py-2.5">
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-medium text-fg">Create missing folders</span>
              <InfoTip content="Automatically creates share folders missing on this host (their parent folder must exist)." />
            </div>
            <Switch
              checked={createMissingFolders}
              onChange={setCreateMissingFolders}
              aria-label="Create missing folders"
            />
          </div>

          {preview && (
            <div className="flex flex-col gap-2.5 rounded-xl border border-border bg-bg p-3.5 text-xs">
              <div className="flex items-center justify-between gap-2">
                <span className="inline-flex items-center gap-1.5 font-semibold text-fg">
                  <CheckCircle2 className="size-4 text-success" />
                  {preview.applied ? 'Restore applied' : 'Dry-run summary'}
                </span>
                <Badge tone="neutral">{preview.hostname || preview.app}</Badge>
              </div>
              <div className="flex flex-wrap gap-2 text-fg-muted tabular-nums">
                <Badge tone="info">{preview.users} users</Badge>
                <Badge tone="info">{preview.shares} shares</Badge>
                <Badge tone="neutral">{preview.smbUsers} SMB creds</Badge>
                <span className="ml-auto text-fg-subtle">{formatDateTime(preview.createdAt)}</span>
              </div>
              {preview.missingFolders.length > 0 && (
                <div className="rounded-md bg-warning-soft px-2.5 py-1.5 text-warning">
                  Missing folders: {preview.missingFolders.join(', ')}
                </div>
              )}
              {preview.warnings.length > 0 && (
                <ul className="list-disc pl-4 text-warning">
                  {preview.warnings.map((w) => (
                    <li key={w}>{w}</li>
                  ))}
                </ul>
              )}
            </div>
          )}

          <div className="flex justify-end gap-2 pt-1">
            <Button
              variant="secondary"
              disabled={!restoreFile || restorePass.length < 12}
              loading={restore.isPending}
              onClick={() => void runRestore(true)}
            >
              <ShieldCheck className="size-4" />
              Dry run
            </Button>
            <Button
              variant="danger"
              disabled={!restoreFile || restorePass.length < 12}
              loading={restore.isPending}
              onClick={() => void runRestore(false)}
            >
              Restore
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
