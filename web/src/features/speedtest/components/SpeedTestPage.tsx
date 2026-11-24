import { useQuery } from '@tanstack/react-query'
import { ArrowDownToLine, ArrowUpFromLine, Gauge, HardDrive, Play, Timer } from 'lucide-react'
import { useState } from 'react'
import { notify } from '@/features/notifications'
import { sharesQuery } from '@/features/shares'
import { PageHeader } from '@/shared/components'
import { useSession } from '@/shared/session/session'
import { Button, Card, CardContent, CardHeader, CardTitle, InfoTip, Select, StatCard } from '@/shared/ui'
import { measureDownload, measureLatency, measureUpload, useDiskSpeedTest } from '../api'

export function SpeedTestPage() {
  const { can } = useSession()
  const shares = useQuery({ ...sharesQuery({ pageSize: 200 }), enabled: can('shares:read') })
  const diskTest = useDiskSpeedTest()

  // Network test state
  const [netMiB, setNetMiB] = useState('32')
  const [netRunning, setNetRunning] = useState(false)
  const [netPhase, setNetPhase] = useState('')
  const [netProgress, setNetProgress] = useState(0)
  const [latency, setLatency] = useState<{ medianMs: number; jitterMs: number } | null>(null)
  const [download, setDownload] = useState<{ mbps: number; mBps: number } | null>(null)
  const [upload, setUpload] = useState<{ mbps: number; mBps: number } | null>(null)

  // Disk test state
  const [diskPath, setDiskPath] = useState('')
  const [diskMiB, setDiskMiB] = useState('32')

  async function runNetworkTest() {
    const size = Number.parseInt(netMiB, 10) || 32
    setNetRunning(true)
    setNetProgress(0)
    try {
      setNetPhase('Measuring latency…')
      const lat = await measureLatency()
      setLatency(lat)

      setNetPhase(`Testing download (${size} MiB)…`)
      const dl = await measureDownload(size, (pct, liveMbps) => {
        setNetProgress(pct)
        setDownload({ mbps: liveMbps, mBps: Math.round((liveMbps / 8) * 10) / 10 })
      })
      setDownload({ mbps: dl.mbps, mBps: dl.mBps })

      setNetPhase(`Testing upload (${size} MiB)…`)
      setNetProgress(50)
      const ul = await measureUpload(size)
      setUpload({ mbps: ul.mbps, mBps: ul.mBps })
      setNetProgress(100)
      setNetPhase('Complete')
    } catch (err) {
      notify.error(err)
      setNetPhase('')
    } finally {
      setNetRunning(false)
    }
  }

  function runDiskBenchmark() {
    diskTest.mutate(
      {
        path: diskPath || undefined,
        sizeMiB: Number.parseInt(diskMiB, 10) || 32,
      },
      {
        onSuccess: (res) => notify.success('Disk benchmark complete', `Write ${res.writeMBps} MB/s · Read ${res.readMBps} MB/s`),
        onError: (err) => notify.error(err),
      },
    )
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Speed Test"
        info="Measure browser-to-server LAN throughput (download, upload, latency) and sequential server disk write/read performance."
      />

      {/* Network Speed Test */}
      <Card>
        <CardHeader className="flex-row flex-wrap items-center justify-between gap-3 pb-4">
          <CardTitle className="flex items-center gap-2">
            <Gauge className="size-4 text-primary" />
            Network Throughput
            <InfoTip content="Streams raw binary chunks directly between your browser and NetShare Hub to estimate real LAN transfer speeds." />
          </CardTitle>
          <div className="flex items-center gap-2">
            <Select className="h-8 w-28 text-xs" value={netMiB} onChange={(e) => setNetMiB(e.target.value)} disabled={netRunning}>
              <option value="16">16 MiB</option>
              <option value="32">32 MiB</option>
              <option value="64">64 MiB</option>
              <option value="128">128 MiB</option>
            </Select>
            <Button size="sm" loading={netRunning} onClick={() => void runNetworkTest()}>
              <Play className="size-3.5" />
              Run test
            </Button>
          </div>
        </CardHeader>
        <CardContent className="flex flex-col gap-4 border-t border-border pt-4">
          {netRunning && (
            <div className="flex flex-col gap-1.5">
              <div className="flex justify-between text-xs text-fg-muted">
                <span>{netPhase}</span>
                <span className="tabular-nums">{netProgress}%</span>
              </div>
              <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-3">
                <div className="h-full bg-primary transition-all duration-150" style={{ width: `${netProgress}%` }} />
              </div>
            </div>
          )}

          <div className="grid gap-4 sm:grid-cols-3">
            <StatCard
              label="Latency"
              value={latency ? `${latency.medianMs} ms` : '—'}
              detail={latency ? `Jitter ${latency.jitterMs} ms` : '5-sample median'}
              icon={<Timer />}
              tone="info"
            />
            <StatCard
              label="Download"
              value={download ? `${download.mbps} Mbps` : '—'}
              detail={download ? `${download.mBps} MB/s` : 'Server → browser'}
              icon={<ArrowDownToLine />}
              tone="success"
            />
            <StatCard
              label="Upload"
              value={upload ? `${upload.mbps} Mbps` : '—'}
              detail={upload ? `${upload.mBps} MB/s` : 'Browser → server'}
              icon={<ArrowUpFromLine />}
              tone="warning"
            />
          </div>
        </CardContent>
      </Card>

      {/* Server Disk Speed Test */}
      <Card>
        <CardHeader className="flex-row flex-wrap items-center justify-between gap-3 pb-4">
          <CardTitle className="flex items-center gap-2">
            <HardDrive className="size-4 text-primary" />
            Server Disk Benchmark
            <InfoTip content="Writes a temporary non-zero file with fsync() and reads it back sequentially in the selected folder, then deletes it immediately." />
          </CardTitle>
          <div className="flex flex-wrap items-center gap-2">
            <Select
              className="h-8 w-52 text-xs"
              value={diskPath}
              onChange={(e) => setDiskPath(e.target.value)}
              disabled={diskTest.isPending}
              aria-label="Target mount"
            >
              <option value="">Server data directory</option>
              {(shares.data?.items ?? []).map((s) => (
                <option key={s.id} value={s.path}>
                  {s.name} ({s.path})
                </option>
              ))}
            </Select>
            <Select
              className="h-8 w-28 text-xs"
              value={diskMiB}
              onChange={(e) => setDiskMiB(e.target.value)}
              disabled={diskTest.isPending}
            >
              <option value="16">16 MiB</option>
              <option value="32">32 MiB</option>
              <option value="64">64 MiB</option>
              <option value="128">128 MiB</option>
            </Select>
            <Button size="sm" loading={diskTest.isPending} onClick={runDiskBenchmark}>
              <Play className="size-3.5" />
              Run disk test
            </Button>
          </div>
        </CardHeader>
        <CardContent className="border-t border-border pt-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <StatCard
              label="Sequential Write (fsync)"
              value={diskTest.data ? `${diskTest.data.writeMBps} MB/s` : '—'}
              detail={diskTest.data ? `${diskTest.data.sizeMiB} MiB in ${diskTest.data.writeMs} ms` : 'Target mount write speed'}
              icon={<HardDrive />}
              tone="info"
            />
            <StatCard
              label="Sequential Read"
              value={diskTest.data ? `${diskTest.data.readMBps} MB/s` : '—'}
              detail={diskTest.data ? `${diskTest.data.sizeMiB} MiB in ${diskTest.data.readMs} ms` : 'Target mount read speed'}
              icon={<HardDrive />}
              tone="success"
            />
          </div>
          {diskTest.data && (
            <div className="mt-3 text-xs text-fg-subtle">
              Tested path: <code className="text-fg-muted">{diskTest.data.path}</code>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
