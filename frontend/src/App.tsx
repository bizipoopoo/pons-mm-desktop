import {useEffect, useMemo, useState} from 'react';
import {
    Activity, AlertTriangle, BarChart3, Check, ChevronDown, ChevronUp, CircleDollarSign,
    Copy, Download, Edit3, Eye, EyeOff, FileCheck2, FileDown, Gauge, KeyRound, LayoutDashboard,
    ListFilter, Lock, LockOpen, LogOut, Play, Plus, Radio, RefreshCw, RotateCcw, Save,
    Settings as SettingsIcon, ShieldCheck, Shuffle, Sparkles, Square, SquareTerminal, StopCircle,
    Trash2, Upload, WalletCards, X,
} from 'lucide-react';
import {
    Bootstrap, CreateFundingTask, CreateVault, DeleteFundingTask, DeleteStrategy, ExitStrategy,
    ExportFundingBatches, ExportGMGN, FetchLatestLaunch, GenerateFundingWallets, GenerateMnemonic,
    ImportMnemonic, ImportPrivateKeys, LockVault, NewStrategy, PreflightStrategy, ResetStrategy,
    RunInitCheck, SaveSettings, SaveStrategy, SetFundingWithdrawCold, StartFundingTask,
    StartStrategy, StopFundingTask, StopStrategy, UnlockVault,
} from '../wailsjs/go/main/App';
import {control, vault} from '../wailsjs/go/models';
import {EventsOn} from '../wailsjs/runtime/runtime';
import './App.css';

type Page = 'overview' | 'strategies' | 'funding' | 'wallets' | 'logs' | 'settings';
type Toast = {kind: 'success' | 'error' | 'info'; text: string};

const short = (value = '') => value.length > 15 ? `${value.slice(0, 7)}...${value.slice(-5)}` : value || '-';
const isActive = (state?: string) => ['starting', 'running', 'stopping', 'exiting'].includes(state || '');
const stateLabel: Record<string, string> = {
    starting: 'Starting', running: 'Running', stopping: 'Stopping', exiting: 'Exiting', stopped: 'Stopped', error: 'Error',
    ready: 'Ready', done: 'Done',
};

function App() {
    const [data, setData] = useState<control.Bootstrap | null>(null);
    const [page, setPage] = useState<Page>('overview');
    const [toast, setToast] = useState<Toast | null>(null);
    const [editing, setEditing] = useState<control.Strategy | null>(null);
    const [liveTarget, setLiveTarget] = useState<control.Strategy | null>(null);
    const [exitTarget, setExitTarget] = useState<control.Strategy | null>(null);
    const [resetTarget, setResetTarget] = useState<control.Strategy | null>(null);
    const [busy, setBusy] = useState('');

    const notify = (kind: Toast['kind'], text: string) => {
        setToast({kind, text});
        window.setTimeout(() => setToast(null), 4200);
    };

    const reload = async () => {
        try { setData(await Bootstrap()); }
        catch (e) { notify('error', String(e)); }
    };

    useEffect(() => {
        reload();
        const unsubscribers = [
            EventsOn('job-updated', (job: control.JobStatus) => setData(prev => prev ? ({...prev, jobs: upsert(prev.jobs || [], job, 'strategyId')}) as control.Bootstrap : prev)),
            EventsOn('job-deleted', (id: string) => setData(prev => prev ? ({...prev, jobs: (prev.jobs || []).filter(j => j.strategyId !== id)}) as control.Bootstrap : prev)),
            EventsOn('strategy-updated', (strategy: control.Strategy) => setData(prev => prev ? ({...prev, strategies: upsert(prev.strategies || [], strategy, 'id')}) as control.Bootstrap : prev)),
            EventsOn('strategy-deleted', (id: string) => setData(prev => prev ? ({...prev, strategies: (prev.strategies || []).filter(s => s.id !== id)}) as control.Bootstrap : prev)),
            EventsOn('strategy-log', (entry: control.LogEntry) => setData(prev => prev ? ({...prev, logs: [...(prev.logs || []), entry].slice(-800)}) as control.Bootstrap : prev)),
            EventsOn('vault-updated', (state: control.VaultState) => setData(prev => prev ? ({...prev, vault: state}) as control.Bootstrap : prev)),
            EventsOn('init-updated', (init: control.InitStatus) => setData(prev => prev ? ({...prev, init}) as control.Bootstrap : prev)),
            EventsOn('funding-updated', (funding: control.FundingState) => setData(prev => prev ? ({...prev, funding}) as control.Bootstrap : prev)),
            EventsOn('funding-task-updated', (task: control.FundingTask) => setData(prev => prev ? ({...prev, funding: {...prev.funding, tasks: upsert(prev.funding?.tasks || [], task, 'id')}}) as control.Bootstrap : prev)),
            EventsOn('funding-task-deleted', (id: string) => setData(prev => prev ? ({...prev, funding: {...prev.funding, tasks: (prev.funding?.tasks || []).filter(t => t.id !== id)}}) as control.Bootstrap : prev)),
            EventsOn('config-updated', reload),
        ];
        return () => unsubscribers.forEach(unsub => unsub());
    }, []);

    const jobs = useMemo(() => new Map((data?.jobs || []).map(j => [j.strategyId, j])), [data?.jobs]);
    const activeCount = [...jobs.values()].filter(j => isActive(j.state)).length;

    const addStrategy = async () => {
        try { setEditing(await NewStrategy()); }
        catch (e) { notify('error', String(e)); }
    };

    const preflight = async (strategy: control.Strategy) => {
        setBusy(`preflight:${strategy.id}`);
        try { notify('success', await PreflightStrategy(strategy.id)); }
        catch (e) { notify('error', String(e)); }
        finally { setBusy(''); }
    };

    const stop = async (id: string) => {
        setBusy(`stop:${id}`);
        try { await StopStrategy(id); notify('info', 'Stop requested'); }
        catch (e) { notify('error', String(e)); }
        finally { setBusy(''); }
    };

    const reset = async (s: control.Strategy) => {
        setBusy(`reset:${s.id}`);
        try { await ResetStrategy(s.id); notify('success', 'Strategy reset; next start will launch a new token'); setResetTarget(null); }
        catch (e) { notify('error', String(e)); }
        finally { setBusy(''); }
    };

    const recheckInit = async () => {
        setBusy('init');
        try { const init = await RunInitCheck(); setData(prev => prev ? ({...prev, init}) as control.Bootstrap : prev); notify(init.ok ? 'success' : 'error', init.message); }
        catch (e) { notify('error', String(e)); }
        finally { setBusy(''); }
    };

    if (!data) return <div className="boot"><Activity className="spin"/> Loading PonsDesk</div>;

    const nav: {id: Page; label: string; icon: typeof Activity}[] = [
        {id: 'overview', label: 'Overview', icon: LayoutDashboard},
        {id: 'strategies', label: 'Strategies', icon: Radio},
        {id: 'funding', label: 'Fund routing', icon: Shuffle},
        {id: 'wallets', label: 'Wallet vault', icon: WalletCards},
        {id: 'logs', label: 'Activity logs', icon: SquareTerminal},
        {id: 'settings', label: 'Settings', icon: SettingsIcon},
    ];

    return <div className="shell">
        <aside className="sidebar">
            <div className="brand"><div className="brand-mark"><BrandMark size={22}/></div><div><strong>PonsDesk</strong><span>Robinhood Chain</span></div></div>
            <nav>{nav.map(item => <button key={item.id} className={page === item.id ? 'active' : ''} onClick={() => setPage(item.id)}><item.icon size={18}/><span>{item.label}</span></button>)}</nav>
            <div className="sidebar-status">
                <div><span className={`dot ${activeCount ? 'live' : ''}`}/><span>{activeCount} live strategies</span></div>
                <div><ShieldCheck size={15}/><span>Chain ID 4663</span></div>
            </div>
        </aside>
        <main>
            <header className="topbar">
                <div><h1>{nav.find(n => n.id === page)?.label}</h1><p>{pageSubtitle(page)}</p></div>
                <div className="top-actions">
                    <button className={`init-chip ${!data.init?.checked ? 'pending' : data.init.ok ? 'ok' : 'failed'}`}
                        title={data.init?.message || 'Startup initialization is running'}
                        disabled={busy === 'init'} onClick={recheckInit}>
                        {!data.init?.checked ? <Activity size={15} className="spin"/> : data.init.ok ? <ShieldCheck size={15}/> : <AlertTriangle size={15}/>}
                        {!data.init?.checked ? 'Initializing' : data.init.ok ? 'Init OK' : 'Init failed'}
                    </button>
                    <button className="icon-button" title="Refresh" onClick={reload}><RefreshCw size={17}/></button>
                    <button className={`vault-chip ${data.vault.unlocked ? 'unlocked' : ''}`} onClick={() => setPage('wallets')}>
                        {data.vault.unlocked ? <LockOpen size={16}/> : <Lock size={16}/>} {data.vault.unlocked ? `${data.vault.wallets?.length || 0} wallets` : 'Vault locked'}
                    </button>
                </div>
            </header>

            <div className="content">
                {page === 'overview' && <Overview data={data} jobs={jobs} onNavigate={setPage} onEdit={setEditing} onStart={setLiveTarget} onStop={stop} onExit={setExitTarget}/>}
                {page === 'strategies' && <Strategies data={data} jobs={jobs} busy={busy} onAdd={addStrategy} onEdit={setEditing} onStart={setLiveTarget} onStop={stop} onExit={setExitTarget} onPreflight={preflight} onReset={setResetTarget} notify={notify}/>}
                {page === 'funding' && <FundingPage state={data.funding} vaultUnlocked={data.vault.unlocked} wallets={data.vault.wallets || []} notify={notify} onNavigate={setPage}/>}
                {page === 'wallets' && <WalletVault state={data.vault} notify={notify}/>}
                {page === 'logs' && <Logs logs={data.logs || []} strategies={data.strategies || []}/>}
                {page === 'settings' && <SettingsPage initial={data.settings} funding={data.funding} vaultUnlocked={data.vault.unlocked} notify={notify}/>}
            </div>
        </main>

        {editing && <StrategyDialog strategy={editing} wallets={data.vault.wallets || []} onClose={() => setEditing(null)} onSaved={saved => {
            setData(prev => prev ? ({...prev, strategies: upsert(prev.strategies || [], saved, 'id')}) as control.Bootstrap : prev);
            setEditing(null); notify('success', 'Strategy saved');
        }} notify={notify}/>}
        {liveTarget && <LiveDialog strategy={liveTarget} onClose={() => setLiveTarget(null)} onConfirm={async () => {
            setBusy(`start:${liveTarget.id}`);
            try { await StartStrategy(liveTarget.id, 'LIVE'); notify('success', `${liveTarget.name} is starting`); setLiveTarget(null); }
            catch (e) { notify('error', String(e)); }
            finally { setBusy(''); }
        }}/>}
        {resetTarget && <ConfirmDialog title="Reset strategy" subtitle={resetTarget.name}
            message="The launched token/pool binding is cleared so the next start launches a fresh token with the same configuration. Only do this after every position is sold; the reset is refused while wallets still hold the token."
            confirmLabel="Reset" busy={busy === `reset:${resetTarget.id}`}
            onClose={() => setResetTarget(null)} onConfirm={() => reset(resetTarget)}/>}
        {exitTarget && <ExitDialog strategy={exitTarget} busy={busy === `exit:${exitTarget.id}`} onClose={() => setExitTarget(null)} onConfirm={async () => {
            setBusy(`exit:${exitTarget.id}`);
            try { await ExitStrategy(exitTarget.id, 'EXIT'); notify('success', `${exitTarget.name} exited all token positions`); setExitTarget(null); }
            catch (e) { notify('error', String(e)); }
            finally { setBusy(''); }
        }}/>}
        {toast && <div className={`toast ${toast.kind}`}>{toast.kind === 'error' ? <AlertTriangle size={17}/> : <Check size={17}/>}<span>{toast.text}</span></div>}
    </div>;
}

function Overview({data, jobs, onNavigate, onEdit, onStart, onStop, onExit}: {
    data: control.Bootstrap; jobs: Map<string, control.JobStatus>; onNavigate: (p: Page) => void;
    onEdit: (s: control.Strategy) => void; onStart: (s: control.Strategy) => void; onStop: (id: string) => void; onExit: (s: control.Strategy) => void;
}) {
    const running = [...jobs.values()].filter(j => isActive(j.state)).length;
    const errors = [...jobs.values()].filter(j => j.state === 'error').length;
    return <>
        <section className="metrics-band">
            <Metric icon={Activity} label="Live strategies" value={String(running)} tone="green"/>
            <Metric icon={CircleDollarSign} label="Configured pairs" value={String(data.strategies?.length || 0)} tone="blue"/>
            <Metric icon={WalletCards} label="Available wallets" value={data.vault.unlocked ? String(data.vault.wallets?.length || 0) : 'Locked'} tone="amber"/>
            <Metric icon={AlertTriangle} label="Task errors" value={String(errors)} tone={errors ? 'red' : 'neutral'}/>
        </section>
        <section className="section-block">
            <div className="section-heading"><div><h2>Strategy control</h2><p>Independent wallet pools can run in parallel.</p></div><button className="secondary" onClick={() => onNavigate('strategies')}><ListFilter size={16}/> Manage</button></div>
            <StrategyTable strategies={data.strategies || []} jobs={jobs} onEdit={onEdit} onStart={onStart} onStop={onStop} onExit={onExit}/>
        </section>
        <section className="split-section">
            <div className="section-block compact"><div className="section-heading"><div><h2>Recent activity</h2><p>Latest engine messages across all pairs.</p></div><button className="icon-button" title="Open logs" onClick={() => onNavigate('logs')}><SquareTerminal size={17}/></button></div>
                <div className="mini-log">{(data.logs || []).slice(-7).reverse().map((log, i) => <div key={`${log.at}-${i}`}><time>{new Date(log.at).toLocaleTimeString()}</time><span className={log.level}>{log.level}</span><p>{log.message}</p></div>)}{!data.logs?.length && <Empty text="No engine activity yet"/>}</div>
            </div>
            <div className="section-block compact"><div className="section-heading"><div><h2>Readiness</h2><p>Required local and network state.</p></div></div>
                <div className="checklist"><CheckRow ok={Boolean(data.init?.ok)} label={data.init?.checked ? (data.init.ok ? 'Startup initialization passed' : 'Startup initialization failed') : 'Startup initialization running'}/><CheckRow ok={Boolean(data.settings.rpcEndpoint)} label="Robinhood RPC configured"/><CheckRow ok={data.vault.unlocked} label="Wallet vault unlocked"/><CheckRow ok={(data.strategies?.length || 0) > 0} label="At least one strategy saved"/><CheckRow ok={(data.vault.wallets?.length || 0) > 1} label="Maker wallets available"/></div>
            </div>
        </section>
    </>;
}

function Strategies({data, jobs, busy, onAdd, onEdit, onStart, onStop, onExit, onPreflight, onReset, notify}: {
    data: control.Bootstrap; jobs: Map<string, control.JobStatus>; busy: string; onAdd: () => void;
    onEdit: (s: control.Strategy) => void; onStart: (s: control.Strategy) => void; onStop: (id: string) => void; onExit: (s: control.Strategy) => void;
    onPreflight: (s: control.Strategy) => void; onReset: (s: control.Strategy) => void; notify: (k: Toast['kind'], t: string) => void;
}) {
    const [expanded, setExpanded] = useState('');
    const [deleteTarget, setDeleteTarget] = useState<control.Strategy | null>(null);
    const [deleting, setDeleting] = useState(false);
    const remove = async (s: control.Strategy) => {
        setDeleting(true);
        try { await DeleteStrategy(s.id); notify('success', 'Strategy deleted'); setDeleteTarget(null); }
        catch (e) { notify('error', String(e)); }
        finally { setDeleting(false); }
    };
    const exportGMGN = async (s: control.Strategy) => {
        try { const path = await ExportGMGN(s.id); notify('success', `Exported to ${path}`); }
        catch (e) { if (!String(e).includes('cancelled')) notify('error', String(e)); }
    };
    return <section className="section-block full-height">
        <div className="section-heading"><div><h2>All strategies</h2><p>Wallet assignments must be disjoint while strategies are live.</p></div><button className="primary" onClick={onAdd}><Plus size={16}/> New strategy</button></div>
        <div className="data-table strategy-list">
            <div className="table-head"><span>Name</span><span>Pair</span><span>Wallets</span><span>Mode</span><span>Status</span><span>Actions</span></div>
            {(data.strategies || []).map(s => {
                const job = jobs.get(s.id); const active = isActive(job?.state);
                const open = expanded === s.id;
                return <div key={s.id} className={`row-group ${open ? 'open' : ''}`}>
                    <div className="table-row">
                        <div className="name-cell"><span className="pair-icon">{(s.token?.symbol || s.name || '?').slice(0, 2).toUpperCase()}</span><div><strong>{s.name}</strong><small>{s.token?.symbol || 'Unlabelled token'}</small></div></div>
                        <div className="mono-cell"><strong>{short(s.tokenAddress)}</strong><small>{short(s.poolAddress)}</small></div>
                        <div><strong>{s.walletIds?.length || 0}</strong><small>1 treasury + {Math.max(0, (s.walletIds?.length || 0) - 1)} makers</small></div>
                        <div><span className="mode-tag">{(s.protocol || 'v1').toUpperCase()} · {s.mode === 'launch' ? 'Launch' : 'Existing'}</span></div>
                        <Status state={job?.state} message={job?.message}/>
                        <div className="row-actions">
                            <button className={open ? 'stats-open' : ''} title="Execution stats" onClick={() => setExpanded(open ? '' : s.id)}><BarChart3 size={16}/></button>
                            <button title="Preflight" disabled={active || busy === `preflight:${s.id}`} onClick={() => onPreflight(s)}><FileCheck2 size={16}/></button>
                            {active ? <><button className="danger-icon" title="Stop without selling" disabled={job?.state === 'exiting'} onClick={() => onStop(s.id)}><StopCircle size={16}/></button>{job?.state === 'running' && <button className="exit-icon" title="One-click exit: sell all" onClick={() => onExit(s)}><LogOut size={16}/></button>}</> : <button className="start-icon" title="Start live" onClick={() => onStart(s)}><Activity size={16}/></button>}
                            <button className="reset-icon" title="Reset: forget the launched token so the next start launches a new one (positions must be fully sold)" disabled={active || busy === `reset:${s.id}`} onClick={() => onReset(s)}><RotateCcw size={16}/></button>
                            <button title="Export GMGN tags" disabled={!data.vault.unlocked} onClick={() => exportGMGN(s)}><Download size={16}/></button>
                            <button title="Edit" disabled={active} onClick={() => onEdit(s)}><Edit3 size={16}/></button>
                            <button title="Delete" disabled={active} onClick={() => setDeleteTarget(s)}><Trash2 size={16}/></button>
                        </div>
                    </div>
                    {open && <StatsPanel stats={job?.stats} state={job?.state}/>}
                </div>;
            })}
            {!data.strategies?.length && <Empty text="No strategies configured" action={<button className="secondary" onClick={onAdd}><Plus size={16}/> Create strategy</button>}/>}
        </div>
        {deleteTarget && <ConfirmDialog title="Delete strategy" subtitle={deleteTarget.name}
            message="The strategy configuration and its job history are removed. Wallets and their balances are not affected."
            confirmLabel="Delete" busy={deleting}
            onClose={() => setDeleteTarget(null)} onConfirm={() => remove(deleteTarget)}/>}
    </section>;
}

function StatsPanel({stats, state}: {stats?: control.JobStats; state?: string}) {
    if (!stats) return <div className="stats-panel"><Empty text="No execution stats yet — they appear once the strategy runs"/></div>;
    const profit = stats.profit ? Number(stats.profit) : null;
    return <div className="stats-panel">
        <div className="stats-grid">
            <StatCell label="Buys" value={String(stats.buyCount ?? 0)} hint="confirmed buy transactions"/>
            <StatCell label="Sells" value={String(stats.sellCount ?? 0)} hint="confirmed sell transactions"/>
            <StatCell label="ETH in" value={`${trimNum(stats.ethSpent)} ETH`} hint="ETH paid into buys"/>
            <StatCell label="Tokens sold" value={trimNum(stats.tokensSold)} hint={`received ${trimNum(stats.ethReceived)} ETH`}/>
            <StatCell label="Total cost" value={`${trimNum(stats.totalCost)} ETH`} hint="gas + tips + launch fee"/>
            <StatCell label="Round P&L" tone={profit == null ? '' : profit >= 0 ? 'pos' : 'neg'}
                value={profit == null ? (isActive(state) ? 'Running…' : '—') : `${profit >= 0 ? '+' : ''}${trimNum(stats.profit)} ETH`}
                hint={stats.startBalance ? `start ${trimNum(stats.startBalance)}${stats.endBalance ? ` → end ${trimNum(stats.endBalance)}` : ''} ETH` : 'balance snapshot at start vs finish'}/>
        </div>
    </div>;
}

function StatCell({label, value, hint, tone = ''}: {label: string; value: string; hint?: string; tone?: string}) {
    return <div className={`stat-cell ${tone}`}><small>{label}</small><strong>{value}</strong>{hint && <span>{hint}</span>}</div>;
}

function trimNum(v?: string) {
    if (!v) return '0';
    const n = Number(v);
    if (!Number.isFinite(n)) return v;
    return n.toLocaleString(undefined, {maximumFractionDigits: 6});
}

function StrategyTable({strategies, jobs, onEdit, onStart, onStop, onExit}: {
    strategies: control.Strategy[]; jobs: Map<string, control.JobStatus>;
    onEdit: (s: control.Strategy) => void; onStart: (s: control.Strategy) => void; onStop: (id: string) => void; onExit: (s: control.Strategy) => void;
}) {
    return <div className="data-table overview-table"><div className="table-head"><span>Strategy</span><span>Token / pool</span><span>Status</span><span>Action</span></div>
        {strategies.slice(0, 8).map(s => { const job = jobs.get(s.id); const active = isActive(job?.state); return <div className="table-row" key={s.id}>
            <div className="name-cell"><span className="pair-icon">{(s.token?.symbol || s.name).slice(0, 2).toUpperCase()}</span><div><strong>{s.name}</strong><small>{s.walletIds?.length || 0} wallets</small></div></div>
            <div className="mono-cell"><strong>{short(s.tokenAddress)}</strong><small>{short(s.poolAddress)}</small></div><Status state={job?.state} message={job?.message}/>
            <div className="row-actions"><button title="Edit" disabled={active} onClick={() => onEdit(s)}><Edit3 size={16}/></button>{active ? <><button className="danger-icon" title="Stop without selling" disabled={job?.state === 'exiting'} onClick={() => onStop(s.id)}><StopCircle size={16}/></button>{job?.state === 'running' && <button className="exit-icon" title="One-click exit: sell all" onClick={() => onExit(s)}><LogOut size={16}/></button>}</> : <button className="start-icon" title="Start live" onClick={() => onStart(s)}><Activity size={16}/></button>}</div>
        </div>; })}
        {!strategies.length && <Empty text="Create a strategy to begin"/>}
    </div>;
}

function StrategyDialog({strategy, wallets, onClose, onSaved, notify}: {
    strategy: control.Strategy; wallets: vault.Summary[]; onClose: () => void;
    onSaved: (s: control.Strategy) => void; notify: (k: Toast['kind'], t: string) => void;
}) {
    const [draft, setDraft] = useState<any>(() => JSON.parse(JSON.stringify(strategy)));
    const [tab, setTab] = useState<'pair' | 'wallets' | 'execution'>('pair');
    const [saving, setSaving] = useState(false);
    const [fetching, setFetching] = useState(false);
    const set = (key: string, value: any) => setDraft((d: any) => ({...d, [key]: value}));
    const setToken = (key: string, value: any) => setDraft((d: any) => ({...d, token: {...(d.token || {}), [key]: value}}));
    const setSocial = (key: string, value: string) => setDraft((d: any) => ({...d, token: {...(d.token || {}), socials: {...(d.token?.socials || {}), [key]: value}}}));
    const selected: string[] = draft.walletIds || [];
    const toggleWallet = (id: string) => set('walletIds', selected.includes(id) ? selected.filter(x => x !== id) : [...selected, id]);
    const selectAllWallets = () => set('walletIds', [...selected, ...wallets.filter(w => !selected.includes(w.id)).map(w => w.id)]);
    const moveWallet = (index: number, direction: -1 | 1) => {
        const next = [...selected]; const target = index + direction; if (target < 0 || target >= next.length) return;
        [next[index], next[target]] = [next[target], next[index]]; set('walletIds', next);
    };
    const prefillFromLatest = async () => {
        setFetching(true);
        try {
            const latest = await FetchLatestLaunch();
            setDraft((d: any) => ({...d, token: {
                ...(d.token || {}),
                name: latest.name || d.token?.name || '',
                symbol: latest.symbol || d.token?.symbol || '',
                logo: latest.logo || '',
                description: latest.description || '',
                socials: {
                    twitter: latest.socials?.twitter || '', telegram: latest.socials?.telegram || '',
                    discord: latest.socials?.discord || '', website: latest.socials?.website || '',
                    farcaster: latest.socials?.farcaster || '',
                },
            }}));
            notify('success', `Prefilled from the latest launch: ${latest.name} (${latest.symbol})`);
        } catch (e) { notify('error', String(e)); }
        finally { setFetching(false); }
    };
    const save = async () => {
        setSaving(true);
        try { onSaved(await SaveStrategy(new control.Strategy(draft))); }
        catch (e) { notify('error', String(e)); }
        finally { setSaving(false); }
    };
    return <Modal title={strategy.id ? `Edit ${strategy.name}` : 'New strategy'} subtitle="All execution parameters are stored locally." onClose={onClose} wide>
        <div className="dialog-tabs"><button className={tab === 'pair' ? 'active' : ''} onClick={() => setTab('pair')}>Pair</button><button className={tab === 'wallets' ? 'active' : ''} onClick={() => setTab('wallets')}>Wallets <span>{selected.length}</span></button><button className={tab === 'execution' ? 'active' : ''} onClick={() => setTab('execution')}>Execution</button></div>
        <div className="dialog-body">
            {tab === 'pair' && <div className="form-stack">
                <div className="form-grid two"><Field label="Strategy name"><input value={draft.name || ''} onChange={e => set('name', e.target.value)} placeholder="Market pair A"/></Field><Field label="Mode"><div className="segmented"><button className={draft.mode === 'existing' ? 'active' : ''} onClick={() => set('mode', 'existing')}>Existing pair</button><button className={draft.mode === 'launch' ? 'active' : ''} onClick={() => set('mode', 'launch')}>New launch</button></div></Field></div>
                <Field label="Pons protocol"><div className="segmented"><button className={draft.protocol === 'v2' ? 'active' : ''} onClick={() => set('protocol', 'v2')}>v2 bonding curve</button><button className={(draft.protocol || 'v1') === 'v1' ? 'active' : ''} onClick={() => set('protocol', 'v1')}>v1 · V3 pool</button></div></Field>
                <div className="inline-note"><Gauge size={17}/><span>{draft.protocol === 'v2' ? 'v2 is the current launch stack. It trades on the bonding curve and stops safely when the token graduates to Uniswap v4.' : 'v1 launches directly into a Uniswap v3 pool and may require a whitelisted deployer while its public gate is closed.'}</span></div>
                {draft.mode === 'existing' ? <div className="form-grid two"><Field label="Token address"><input className="mono" value={draft.tokenAddress || ''} onChange={e => set('tokenAddress', e.target.value)} placeholder="0x..."/></Field><Field label={draft.protocol === 'v2' ? 'Bonding curve address' : 'V3 pool address'}><input className="mono" value={draft.poolAddress || ''} onChange={e => set('poolAddress', e.target.value)} placeholder="0x..."/></Field></div> : <>
                    <button className="ghost-command" disabled={fetching} onClick={prefillFromLatest}><Sparkles size={15}/>{fetching ? 'Fetching the latest launch…' : 'Prefill from the latest launched token (name, logo, description)'}</button>
                    <div className="form-grid two"><Field label="Token name"><input value={draft.token?.name || ''} onChange={e => setToken('name', e.target.value)}/></Field><Field label="Symbol"><input value={draft.token?.symbol || ''} onChange={e => setToken('symbol', e.target.value.toUpperCase())}/></Field></div>
                    <Field label="Logo URL"><input value={draft.token?.logo || ''} onChange={e => setToken('logo', e.target.value)} placeholder="https://.../logo.png"/></Field>
                    <Field label="Description"><textarea rows={3} value={draft.token?.description || ''} onChange={e => setToken('description', e.target.value)}/></Field>
                    <div className="form-grid two"><Field label={draft.protocol === 'v2' ? 'Creator fee recipient (optional)' : 'Fee wallet (optional)'}><input className="mono" value={draft.token?.feeWallet || ''} onChange={e => setToken('feeWallet', e.target.value)} placeholder="Defaults to deployer"/></Field><Field label={draft.protocol === 'v2' ? 'Initial buy (ETH, atomic — lands inside the launch tx, unsnipeable)' : 'Initial buy (ETH)'}><DecimalInput min={0} step={0.001} value={draft.devBuyEth ?? 0} onChange={v => set('devBuyEth', v)}/></Field></div>
                    <div className="form-grid two"><Field label="Website"><input value={draft.token?.socials?.website || ''} onChange={e => setSocial('website', e.target.value)}/></Field><Field label="X / Twitter"><input value={draft.token?.socials?.twitter || ''} onChange={e => setSocial('twitter', e.target.value)}/></Field><Field label="Telegram"><input value={draft.token?.socials?.telegram || ''} onChange={e => setSocial('telegram', e.target.value)}/></Field><Field label="Farcaster"><input value={draft.token?.socials?.farcaster || ''} onChange={e => setSocial('farcaster', e.target.value)}/></Field></div>
                </>}
            </div>}
            {tab === 'wallets' && <div className="wallet-assignment">
                <div className="assignment-header"><div><strong>Execution order</strong><p>The first wallet is treasury/deployer; remaining wallets are makers.</p></div><div className="assignment-actions">
                    <button className="secondary small" disabled={!wallets.length || wallets.every(w => selected.includes(w.id))} onClick={selectAllWallets}><Check size={14}/> Select all</button>
                    <button className="secondary small" disabled={!selected.length} onClick={() => set('walletIds', [])}><X size={14}/> Clear</button>
                </div></div>
                {!wallets.length && <Empty text="Unlock the vault and import wallets first"/>}
                {selected.map((id, index) => { const w = wallets.find(item => item.id === id); if (!w) return null; return <div className="assigned-wallet" key={id}><span className="role-index">{index + 1}</span><div><strong>{w.label}</strong><small className="mono">{w.address}</small></div><span className={`role-tag ${index === 0 ? 'treasury' : ''}`}>{index === 0 ? 'Treasury' : 'Maker'}</span><button title="Move up" disabled={index === 0} onClick={() => moveWallet(index, -1)}><ChevronUp size={15}/></button><button title="Move down" disabled={index === selected.length - 1} onClick={() => moveWallet(index, 1)}><ChevronDown size={15}/></button><button title="Remove" onClick={() => toggleWallet(id)}><X size={15}/></button></div>; })}
                <div className="available-wallets"><strong>Available wallets</strong>{wallets.filter(w => !selected.includes(w.id)).map(w => <button key={w.id} onClick={() => toggleWallet(w.id)}><Plus size={15}/><span>{w.label}</span><small className="mono">{short(w.address)}</small></button>)}</div>
            </div>}
            {tab === 'execution' && <div className="form-stack">
                <SpeedField label="Buy speed" value={draft.accumulateIntervalMs} onChange={v => set('accumulateIntervalMs', v)}/>
                <label className="toggle-line"><input type="checkbox" checked={Boolean(draft.concurrentBuys)} onChange={e => set('concurrentBuys', e.target.checked)}/><span className="toggle"/><div><strong>Concurrent buys</strong><small>Submit one buy from every funded maker in the same round.</small></div></label>
                <SpeedField label="Sell speed" value={draft.sellIntervalMs} onChange={v => set('sellIntervalMs', v)}/>
                <label className="toggle-line"><input type="checkbox" checked={!Boolean(draft.sequentialSells)} onChange={e => set('sequentialSells', !e.target.checked)}/><span className="toggle"/><div><strong>Concurrent sells</strong><small>Enabled by default; clear the wallets in each batch in parallel.</small></div></label>
                <div className="inline-note"><Gauge size={17}/><span>Speed is the minimum interval between rounds. Network confirmation can make the effective interval longer.</span></div>
                <div className="inline-note"><Gauge size={17}/><span>Simplified strategy: every maker buys once. On an outside buy, if the full-exit quote covers all costs (buys + gas + tips + launch fee) with profit, everything is sold concurrently at once; otherwise wallets are cleared in batches of 4-6. If the buyer sells, pumping resumes; if everything is sold first, the strategy stops.</span></div>
                <div className="form-grid three"><NumberField label="Buy fraction" value={draft.buyFraction} step={0.01} min={0.01} max={1} onChange={v => set('buyFraction', v)}/><NumberField label="Chip target · total supply" value={draft.chipTarget} step={0.05} min={0.05} max={1} onChange={v => set('chipTarget', v)}/><NumberField label="Buy slippage (bps)" value={draft.slippageBps} step={50} min={0} max={9999} onChange={v => set('slippageBps', v)}/><NumberField label="Priority tip (gwei)" value={draft.priorityTipGwei} step={0.1} min={0} onChange={v => set('priorityTipGwei', v)}/><NumberField label="Gas reserve (ETH)" value={draft.gasReserveEth} step={0.001} min={0} onChange={v => set('gasReserveEth', v)}/></div>
                <label className="toggle-line"><input type="checkbox" checked={Boolean(draft.graduate)} onChange={e => set('graduate', e.target.checked)}/><span className="toggle"/><div><strong>Continue until graduation threshold</strong><small>Accumulate until paired principal reaches the configured launch threshold.</small></div></label>
            </div>}
        </div>
        <div className="dialog-footer"><button className="secondary" onClick={onClose}>Cancel</button><button className="primary" disabled={saving} onClick={save}><Save size={16}/>{saving ? 'Saving' : 'Save strategy'}</button></div>
    </Modal>;
}

function WalletVault({state, notify}: {state: control.VaultState; notify: (k: Toast['kind'], t: string) => void}) {
    const [password, setPassword] = useState(''); const [show, setShow] = useState(false); const [busy, setBusy] = useState(false);
    const [mode, setMode] = useState<'keys' | 'mnemonic'>('keys'); const [input, setInput] = useState(''); const [count, setCount] = useState(10); const [prefix, setPrefix] = useState('Maker'); const [generated, setGenerated] = useState('');
    const unlock = async () => { setBusy(true); try { state.exists ? await UnlockVault(password) : await CreateVault(password); setPassword(''); notify('success', state.exists ? 'Vault unlocked' : 'Encrypted vault created'); } catch (e) { notify('error', String(e)); } finally { setBusy(false); } };
    const lock = async () => { try { await LockVault(); notify('success', 'Vault locked'); } catch (e) { notify('error', String(e)); } };
    const importWallets = async () => { setBusy(true); try { const added = mode === 'keys' ? await ImportPrivateKeys(input, prefix) : await ImportMnemonic(input, count, prefix); setInput(''); notify('success', `Imported ${added.length} wallets`); } catch (e) { notify('error', String(e)); } finally { setBusy(false); } };
    const generate = async () => { try { setGenerated(await GenerateMnemonic()); } catch (e) { notify('error', String(e)); } };
    if (!state.unlocked) return <section className="vault-gate"><div className="vault-symbol"><KeyRound size={28}/></div><h2>{state.exists ? 'Unlock wallet vault' : 'Create encrypted wallet vault'}</h2><p>Trading keys stay encrypted in the local PonsDesk data directory.</p><div className="password-input"><input type={show ? 'text' : 'password'} value={password} onChange={e => setPassword(e.target.value)} onKeyDown={e => e.key === 'Enter' && unlock()} placeholder="Vault password"/><button title={show ? 'Hide password' : 'Show password'} onClick={() => setShow(!show)}>{show ? <EyeOff size={17}/> : <Eye size={17}/>}</button></div><button className="primary" disabled={busy || password.length < 8} onClick={unlock}>{state.exists ? <LockOpen size={16}/> : <ShieldCheck size={16}/>} {busy ? 'Working' : state.exists ? 'Unlock vault' : 'Create vault'}</button></section>;
    return <div className="wallet-layout">
        <section className="section-block wallet-table"><div className="section-heading"><div><h2>Wallet inventory</h2><p>{state.wallets?.length || 0} encrypted EVM signing accounts</p></div><button className="secondary" onClick={lock}><Lock size={16}/> Lock</button></div>
            <div className="data-table"><div className="table-head"><span>Label</span><span>Address</span><span>Source</span></div>{(state.wallets || []).map(w => <div className="table-row" key={w.id}><div className="name-cell"><span className="wallet-avatar"><WalletCards size={16}/></span><strong>{w.label}</strong></div><span className="mono full-address">{w.address}</span><span className="mode-tag">Encrypted</span></div>)}</div>
        </section>
        <section className="section-block import-panel"><div className="section-heading"><div><h2>Import wallets</h2><p>New keys are encrypted immediately.</p></div></div><div className="segmented"><button className={mode === 'keys' ? 'active' : ''} onClick={() => setMode('keys')}>Private keys</button><button className={mode === 'mnemonic' ? 'active' : ''} onClick={() => setMode('mnemonic')}>Mnemonic</button></div>
            <Field label="Label prefix"><input value={prefix} onChange={e => setPrefix(e.target.value)}/></Field>
            {mode === 'keys' ? <Field label="Private keys"><textarea className="secret-area" rows={8} value={input} onChange={e => setInput(e.target.value)} placeholder="One private key per line"/></Field> : <><Field label="Recovery phrase"><textarea className="secret-area" rows={5} value={input} onChange={e => setInput(e.target.value)} placeholder="12 or 24 words"/></Field><NumberField label="Addresses to derive" value={count} step={1} min={1} max={2000} onChange={setCount}/><button className="ghost-command" onClick={generate}><RefreshCw size={15}/> Generate a new 24-word phrase</button></>}
            <button className="primary wide-button" disabled={busy || !input.trim()} onClick={importWallets}><Upload size={16}/>{busy ? 'Importing' : 'Import and encrypt'}</button>
        </section>
        {generated && <Modal title="New recovery phrase" subtitle="This phrase is shown once and is not stored by PonsDesk." onClose={() => setGenerated('')}><div className="recovery-phrase">{generated}</div><div className="dialog-footer"><button className="secondary" onClick={() => navigator.clipboard.writeText(generated)}><Copy size={16}/> Copy</button><button className="primary" onClick={() => {setInput(generated); setGenerated('');}}>Use this phrase</button></div></Modal>}
    </div>;
}

function Logs({logs, strategies}: {logs: control.LogEntry[]; strategies: control.Strategy[]}) {
    const [filter, setFilter] = useState(''); const [level, setLevel] = useState('all');
    const names = new Map(strategies.map(s => [s.id, s.name]));
    const shown = logs.filter(l => (level === 'all' || l.level === level) && (!filter || l.message.toLowerCase().includes(filter.toLowerCase()) || (names.get(l.strategyId) || '').toLowerCase().includes(filter.toLowerCase()))).slice().reverse();
    return <section className="section-block full-height logs-section"><div className="section-heading"><div><h2>Engine activity</h2><p>Bounded in-memory log; secrets are never included.</p></div><div className="log-filters"><select value={level} onChange={e => setLevel(e.target.value)}><option value="all">All levels</option><option value="info">Info</option><option value="warn">Warn</option><option value="error">Error</option></select><input value={filter} onChange={e => setFilter(e.target.value)} placeholder="Filter logs"/></div></div><div className="log-console">{shown.map((log, i) => <div className="log-row" key={`${log.at}-${i}`}><time>{new Date(log.at).toLocaleTimeString()}</time><span className={`log-level ${log.level}`}>{log.level}</span><span className="log-source">{names.get(log.strategyId) || short(log.strategyId)}</span><p>{log.message}</p></div>)}{!shown.length && <Empty text="No matching log entries"/>}</div></section>;
}

function SettingsPage({initial, funding, vaultUnlocked, notify}: {
    initial: control.Settings; funding: control.FundingState; vaultUnlocked: boolean; notify: (k: Toast['kind'], t: string) => void;
}) {
    const [settings, setSettings] = useState<any>(() => ({...initial})); const [showRPC, setShowRPC] = useState(false); const [saving, setSaving] = useState(false);
    const save = async () => { setSaving(true); try { await SaveSettings(new control.Settings(settings)); notify('success', 'Settings saved'); } catch (e) { notify('error', String(e)); } finally { setSaving(false); } };
    return <section className="settings-layout"><div className="section-block"><div className="section-heading"><div><h2>Network</h2><p>Shared defaults for all configured pairs.</p></div></div><Field label="Robinhood Chain RPC"><div className="password-input"><input className="mono" type={showRPC ? 'text' : 'password'} value={settings.rpcEndpoint || ''} onChange={e => setSettings({...settings, rpcEndpoint: e.target.value})} placeholder="wss://..."/><button title={showRPC ? 'Hide endpoint' : 'Reveal endpoint'} onClick={() => setShowRPC(!showRPC)}>{showRPC ? <EyeOff size={17}/> : <Eye size={17}/>}</button></div></Field><div className="inline-note"><Gauge size={17}/><span>WebSocket endpoints enable event-driven pool monitoring. HTTPS falls back to polling.</span></div></div>
        <div className="section-block"><div className="section-heading"><div><h2>GMGN viewer</h2><p>Optional account used when manually importing generated wallet tags.</p></div></div><Field label="Viewer wallet address"><input className="mono" value={settings.gmgnViewerWallet || ''} onChange={e => setSettings({...settings, gmgnViewerWallet: e.target.value})} placeholder="0x..."/></Field></div>
        <div className="settings-actions"><button className="primary" disabled={saving} onClick={save}><Save size={16}/>{saving ? 'Saving' : 'Save settings'}</button></div>
        <FundingSettings config={funding?.config} vaultUnlocked={vaultUnlocked} notify={notify}/>
    </section>;
}

function fundingConfigured(config?: control.FundingConfig) {
    return Boolean(config?.depositCold && (config?.depositRelays?.length || 0) === 10 && (config?.withdrawRelays?.length || 0) === 10 && config?.withdrawCold);
}

function FundingSettings({config, vaultUnlocked, notify}: {config?: control.FundingConfig; vaultUnlocked: boolean; notify: (k: Toast['kind'], t: string) => void}) {
    const [busy, setBusy] = useState('');
    const [coldAddr, setColdAddr] = useState('');
    const generate = async (role: string) => {
        setBusy(role);
        try { const path = await GenerateFundingWallets(role); notify('success', `Generated and backed up to ${path}`); }
        catch (e) { if (!String(e).includes('cancelled')) notify('error', String(e)); }
        finally { setBusy(''); }
    };
    const saveCold = async () => {
        setBusy('withdraw-cold');
        try { await SetFundingWithdrawCold(coldAddr); notify('success', 'Withdraw cold address saved'); setColdAddr(''); }
        catch (e) { notify('error', String(e)); }
        finally { setBusy(''); }
    };
    return <div className="section-block"><div className="section-heading"><div><h2>Fund routing wallets</h2><p>Fixed once configured. Generated keys are encrypted in the vault; a backup download is mandatory.</p></div></div>
        {!vaultUnlocked && <div className="inline-note"><Lock size={17}/><span>Unlock the wallet vault before generating routing wallets.</span></div>}
        <div className="funding-roles">
            <FundingRole label="Deposit cold wallet" hint="Receives the money you want to distribute" done={Boolean(config?.depositCold)}
                detail={config?.depositCold ? config.depositCold.address : 'Not configured'}
                action={<button className="secondary small" disabled={!vaultUnlocked || busy === 'deposit-cold'} onClick={() => generate('deposit-cold')}><KeyRound size={14}/> Generate</button>}/>
            <FundingRole label="Deposit relay wallets" hint="10 intermediaries between the cold wallet and batch 1" done={(config?.depositRelays?.length || 0) === 10}
                detail={(config?.depositRelays?.length || 0) === 10 ? `10 wallets · ${short(config!.depositRelays![0].address)} …` : 'Not configured'}
                action={<button className="secondary small" disabled={!vaultUnlocked || busy === 'deposit-relays'} onClick={() => generate('deposit-relays')}><KeyRound size={14}/> Generate 10</button>}/>
            <FundingRole label="Withdraw relay wallets" hint="10 intermediaries that gather funds before the final payout" done={(config?.withdrawRelays?.length || 0) === 10}
                detail={(config?.withdrawRelays?.length || 0) === 10 ? `10 wallets · ${short(config!.withdrawRelays![0].address)} …` : 'Not configured'}
                action={<button className="secondary small" disabled={!vaultUnlocked || busy === 'withdraw-relays'} onClick={() => generate('withdraw-relays')}><KeyRound size={14}/> Generate 10</button>}/>
            <FundingRole label="Withdraw cold wallet" hint="Address only — its key is never stored here" done={Boolean(config?.withdrawCold)}
                detail={config?.withdrawCold || 'Not configured'}
                action={!config?.withdrawCold ? <div className="funding-cold-input"><input className="mono" value={coldAddr} onChange={e => setColdAddr(e.target.value)} placeholder="0x..."/><button className="secondary small" disabled={!coldAddr.trim() || busy === 'withdraw-cold'} onClick={saveCold}><Save size={14}/> Save</button></div> : null}/>
        </div>
        {fundingConfigured(config) && <div className="inline-note"><ShieldCheck size={17}/><span>All routing wallets are configured. Distribution and withdrawal tasks are available on the Fund routing page.</span></div>}
    </div>;
}

function FundingRole({label, hint, detail, done, action}: {label: string; hint: string; detail: string; done: boolean; action: React.ReactNode}) {
    return <div className={`funding-role ${done ? 'done' : ''}`}>
        <span className="funding-role-state">{done ? <Check size={16}/> : <Square size={16}/>}</span>
        <div className="funding-role-text"><strong>{label}</strong><small>{hint}</small><span className="mono">{detail}</span></div>
        {!done && <div className="funding-role-action">{action}</div>}
    </div>;
}

function FundingPage({state, vaultUnlocked, wallets, notify, onNavigate}: {
    state: control.FundingState; vaultUnlocked: boolean; wallets: vault.Summary[]; notify: (k: Toast['kind'], t: string) => void; onNavigate: (p: Page) => void;
}) {
    const [creating, setCreating] = useState(false);
    const [startTarget, setStartTarget] = useState<control.FundingTask | null>(null);
    const [deleteTarget, setDeleteTarget] = useState<control.FundingTask | null>(null);
    const [busy, setBusy] = useState('');
    const configured = fundingConfigured(state?.config);
    const tasks = state?.tasks || [];

    const exportBatches = async (task: control.FundingTask) => {
        try { const path = await ExportFundingBatches(task.id); notify('success', `Batch mnemonics saved to ${path}`); }
        catch (e) { if (!String(e).includes('cancelled')) notify('error', String(e)); }
    };
    const stop = async (task: control.FundingTask) => {
        setBusy(`stop:${task.id}`);
        try { await StopFundingTask(task.id); notify('info', 'Stop requested; the current hop finishes safely'); }
        catch (e) { notify('error', String(e)); }
        finally { setBusy(''); }
    };
    const remove = async (task: control.FundingTask) => {
        setBusy(`delete:${task.id}`);
        try { await DeleteFundingTask(task.id); notify('success', 'Task deleted'); setDeleteTarget(null); }
        catch (e) { notify('error', String(e)); }
        finally { setBusy(''); }
    };

    if (!configured) return <section className="section-block full-height"><div className="section-heading"><div><h2>Fund routing</h2><p>Distribute or withdraw native coin through 5 relay batches.</p></div></div>
        <Empty text="Configure the deposit/withdraw routing wallets first" action={<button className="secondary" onClick={() => onNavigate('settings')}><SettingsIcon size={16}/> Open Settings</button>}/>
    </section>;

    return <section className="section-block full-height">
        <div className="section-heading"><div><h2>Routing tasks</h2><p>Distribute: cold → 10 relays → 5 batches → targets. Withdraw: sources → 5 batches → 10 relays → cold. Restarting a stopped task continues from the current chain state.</p></div>
            <button className="primary" onClick={() => setCreating(true)}><Plus size={16}/> New task</button></div>
        <div className="funding-route-summary">
            <span className="mode-tag">Deposit cold {short(state.config?.depositCold?.address)}</span>
            <span className="mode-tag">10 deposit relays</span>
            <span className="mode-tag">10 withdraw relays</span>
            <span className="mode-tag">Withdraw cold {short(state.config?.withdrawCold)}</span>
        </div>
        <div className="data-table funding-table">
            <div className="table-head"><span>Task</span><span>Wallets</span><span>Progress</span><span>Status</span><span>Actions</span></div>
            {tasks.map(task => {
                const running = task.state === 'running';
                const pct = task.transfersTotal ? Math.round(100 * (task.transfersDone || 0) / task.transfersTotal) : 0;
                return <div className="table-row" key={task.id}>
                    <div className="name-cell"><span className="pair-icon">{task.kind === 'distribute' ? 'DN' : 'UP'}</span><div><strong>{task.kind === 'distribute' ? 'Distribute' : 'Withdraw'}</strong><small>{new Date(task.createdAt).toLocaleString()}</small></div></div>
                    <div><strong>{task.targets?.length || 0}</strong><small>{task.kind === 'distribute' ? 'target wallets' : 'source wallets'} · 5×{task.targets?.length || 0} temp</small></div>
                    <div className="funding-progress"><div className="progress-track"><div className="progress-fill" style={{width: `${pct}%`}}/></div><small>{task.transfersDone || 0}/{task.transfersTotal || 0} transfers · hop {task.hopsDone || 0}/{task.hopsTotal || 0}</small></div>
                    <Status state={task.state} message={task.message}/>
                    <div className="row-actions">
                        <button title="Download the 5 batch mnemonics" onClick={() => exportBatches(task)}><FileDown size={16}/></button>
                        {running ? <button className="danger-icon" title="Stop after the current transfers confirm" disabled={busy === `stop:${task.id}`} onClick={() => stop(task)}><StopCircle size={16}/></button>
                            : <button className="start-icon" title={task.state === 'done' ? 'Completed — start re-checks and moves any remaining balance' : 'Start'} disabled={!vaultUnlocked} onClick={() => setStartTarget(task)}><Play size={16}/></button>}
                        <button title="Delete" disabled={running} onClick={() => setDeleteTarget(task)}><Trash2 size={16}/></button>
                    </div>
                </div>;
            })}
            {!tasks.length && <Empty text="No routing tasks yet" action={<button className="secondary" onClick={() => setCreating(true)}><Plus size={16}/> Create task</button>}/>}
        </div>
        {!vaultUnlocked && <div className="inline-note"><Lock size={17}/><span>Unlock the wallet vault to start tasks; every hop signs transactions.</span></div>}
        {creating && <FundingTaskDialog vaultUnlocked={vaultUnlocked} wallets={selectableWallets(wallets, state.config)} onClose={() => setCreating(false)} notify={notify} onCreated={task => { setCreating(false); notify('success', 'Task created — download the batch mnemonics before starting'); void exportBatches(task); }}/>}
        {startTarget && <FundingStartDialog task={startTarget} busy={busy === `start:${startTarget.id}`} onClose={() => setStartTarget(null)} onConfirm={async () => {
            setBusy(`start:${startTarget.id}`);
            try { await StartFundingTask(startTarget.id, 'SEND'); notify('success', 'Routing task started'); setStartTarget(null); }
            catch (e) { notify('error', String(e)); }
            finally { setBusy(''); }
        }}/>}
        {deleteTarget && <ConfirmDialog title="Delete routing task" subtitle={deleteTarget.kind === 'distribute' ? 'Distribute' : 'Withdraw'}
            message="The task record and its batch mnemonics are removed from this app. Make sure the batch mnemonics are downloaded if any temporary wallet could still hold funds."
            confirmLabel="Delete" busy={busy === `delete:${deleteTarget.id}`}
            onClose={() => setDeleteTarget(null)} onConfirm={() => remove(deleteTarget)}/>}
    </section>;
}

// selectableWallets hides the funding routing wallets (cold + relays) from the
// task pickers so the route's own infrastructure can't be chosen as an endpoint.
function selectableWallets(wallets: vault.Summary[], config?: control.FundingConfig) {
    const excluded = new Set<string>();
    if (config?.depositCold) excluded.add(config.depositCold.id);
    for (const w of config?.depositRelays || []) excluded.add(w.id);
    for (const w of config?.withdrawRelays || []) excluded.add(w.id);
    return wallets.filter(w => !excluded.has(w.id));
}

function FundingTaskDialog({vaultUnlocked, wallets, onClose, onCreated, notify}: {
    vaultUnlocked: boolean; wallets: vault.Summary[]; onClose: () => void; onCreated: (t: control.FundingTask) => void; notify: (k: Toast['kind'], t: string) => void;
}) {
    const [kind, setKind] = useState<'distribute' | 'withdraw'>('distribute');
    const [selected, setSelected] = useState<string[]>([]);
    const [extraKeys, setExtraKeys] = useState('');
    const [saving, setSaving] = useState(false);
    const toggle = (id: string) => setSelected(prev => prev.includes(id) ? prev.filter(x => x !== id) : [...prev, id]);
    const keyCount = extraKeys.split(/[\s,;]+/).filter(Boolean).length;
    const total = selected.length + (kind === 'withdraw' ? keyCount : 0);
    const create = async () => {
        setSaving(true);
        try {
            const picked = wallets.filter(w => selected.includes(w.id)).map(w => w.address);
            const input = kind === 'withdraw' ? [...picked, ...extraKeys.split(/[\s,;]+/).filter(Boolean)].join('\n') : picked.join('\n');
            onCreated(await CreateFundingTask(kind, input));
        }
        catch (e) { notify('error', String(e)); }
        finally { setSaving(false); }
    };
    return <Modal title="New routing task" subtitle="5 temporary relay batches are generated per task; every slot follows one fixed path." onClose={onClose} wide>
        <div className="dialog-body"><div className="form-stack">
            <Field label="Direction"><div className="segmented">
                <button className={kind === 'distribute' ? 'active' : ''} onClick={() => setKind('distribute')}>Distribute · cold → targets</button>
                <button className={kind === 'withdraw' ? 'active' : ''} onClick={() => setKind('withdraw')}>Withdraw · sources → cold</button>
            </div></Field>
            {kind === 'distribute'
                ? <div className="inline-note"><Gauge size={17}/><span>Fund the deposit cold wallet first. Its full spendable balance is split randomly but near-evenly across the 10 relays, then into batch 1, then forwarded 1:1 through batches 2-5 to the selected wallets.</span></div>
                : <div className="inline-note"><Gauge size={17}/><span>Each source wallet moves its full balance 1:1 through batches 1-5, then the last batch gathers into the 10 withdraw relays, which pay the withdraw cold address.</span></div>}
            {!vaultUnlocked ? <div className="inline-note"><Lock size={17}/><span>Unlock the wallet vault to pick wallets.</span></div> : <>
                <div className="assignment-header"><div><strong>{kind === 'distribute' ? `Target wallets (${selected.length})` : `Source wallets (${selected.length})`}</strong><p>Pick from the wallet vault; funding routing wallets are hidden.</p></div><div className="assignment-actions">
                    <button className="secondary small" disabled={!wallets.length || selected.length === wallets.length} onClick={() => setSelected(wallets.map(w => w.id))}><Check size={14}/> Select all</button>
                    <button className="secondary small" disabled={!selected.length} onClick={() => setSelected([])}><X size={14}/> Clear</button>
                </div></div>
                <div className="funding-pick-list">
                    {wallets.map(w => { const on = selected.includes(w.id); return <button key={w.id} className={on ? 'picked' : ''} onClick={() => toggle(w.id)}>
                        {on ? <Check size={15}/> : <Square size={15}/>}<span>{w.label}</span><small className="mono">{short(w.address)}</small>
                    </button>; })}
                    {!wallets.length && <Empty text="No selectable wallets in the vault"/>}
                </div>
            </>}
            {kind === 'withdraw' && <Field label={`Additional source private keys (${keyCount}) — optional, for wallets not yet in the vault`}>
                <textarea className="secret-area" rows={4} value={extraKeys} onChange={e => setExtraKeys(e.target.value)} placeholder="One private key per line; they are encrypted into the vault on create"/>
            </Field>}
        </div></div>
        <div className="dialog-footer"><button className="secondary" onClick={onClose}>Cancel</button>
            <button className="primary" disabled={saving || !total || !vaultUnlocked} onClick={create}><Plus size={16}/>{saving ? 'Creating' : `Create task (${total})`}</button></div>
    </Modal>;
}

function FundingStartDialog({task, busy, onClose, onConfirm}: {task: control.FundingTask; busy: boolean; onClose: () => void; onConfirm: () => void}) {
    const [phrase, setPhrase] = useState('');
    return <Modal title={task.kind === 'distribute' ? 'Start distribution' : 'Start withdrawal'} subtitle={`${task.targets?.length || 0} wallets · ${task.transfersTotal} transfers`} onClose={busy ? () => {} : onClose}>
        <div className="risk-box"><AlertTriangle size={20}/><div><strong>This moves real native coin</strong>
            <p>{task.kind === 'distribute'
                ? 'The deposit cold wallet\'s full spendable balance is routed through the relays and 5 temporary batches to the target addresses. Each hop pays its own gas from the carried funds.'
                : 'Every source wallet\'s full balance is routed through 5 temporary batches and the withdraw relays to the withdraw cold address. Each hop pays its own gas from the carried funds.'}</p>
            <p>Download the batch mnemonics first — they are the only way to recover funds from the temporary wallets outside this app.</p></div></div>
        <Field label="Type SEND to confirm"><input autoFocus disabled={busy} value={phrase} onChange={e => setPhrase(e.target.value.toUpperCase())}/></Field>
        <div className="dialog-footer"><button className="secondary" disabled={busy} onClick={onClose}>Cancel</button>
            <button className="danger" disabled={busy || phrase !== 'SEND'} onClick={onConfirm}><Play size={16}/>{busy ? 'Starting' : 'Start routing'}</button></div>
    </Modal>;
}

function LiveDialog({strategy, onClose, onConfirm}: {strategy: control.Strategy; onClose: () => void; onConfirm: () => void}) {
    const [phrase, setPhrase] = useState('');
    return <Modal title={strategy.mode === 'launch' ? 'Launch token and start market maker' : 'Start live market maker'} subtitle={strategy.name} onClose={onClose}><div className="risk-box"><AlertTriangle size={20}/><div><strong>This sends real transactions</strong><p>Selected wallets can spend ETH, pay gas, buy, approve, and sell tokens according to this strategy.</p></div></div><Field label="Type LIVE to confirm"><input autoFocus value={phrase} onChange={e => setPhrase(e.target.value.toUpperCase())}/></Field><div className="dialog-footer"><button className="secondary" onClick={onClose}>Cancel</button><button className="danger" disabled={phrase !== 'LIVE'} onClick={onConfirm}><Activity size={16}/>{strategy.mode === 'launch' ? 'Launch and run' : 'Start live'}</button></div></Modal>;
}

function ExitDialog({strategy, busy, onClose, onConfirm}: {strategy: control.Strategy; busy: boolean; onClose: () => void; onConfirm: () => void}) {
    const [phrase, setPhrase] = useState('');
    return <Modal title="One-click exit" subtitle={strategy.name} onClose={busy ? () => {} : onClose}><div className="risk-box"><AlertTriangle size={20}/><div><strong>This sells every token balance</strong><p>PonsDesk will stop normal strategy decisions and concurrently batch-sell 100% of the token held by the treasury and all maker wallets. This action overrides the normal sell concurrency setting, pays gas, may require approvals, and cannot be undone.</p></div></div><Field label="Type EXIT to confirm"><input autoFocus disabled={busy} value={phrase} onChange={e => setPhrase(e.target.value.toUpperCase())}/></Field><div className="dialog-footer"><button className="secondary" disabled={busy} onClick={onClose}>Cancel</button><button className="danger" disabled={busy || phrase !== 'EXIT'} onClick={onConfirm}><LogOut size={16}/>{busy ? 'Selling all positions' : 'Exit all positions'}</button></div></Modal>;
}

// ConfirmDialog replaces window.confirm, which the Wails WebView does not
// implement (it silently returns false, so the action would never run).
function ConfirmDialog({title, subtitle, message, confirmLabel, busy, onClose, onConfirm}: {
    title: string; subtitle?: string; message: string; confirmLabel: string; busy: boolean; onClose: () => void; onConfirm: () => void;
}) {
    return <Modal title={title} subtitle={subtitle} onClose={busy ? () => {} : onClose}>
        <div className="risk-box"><AlertTriangle size={20}/><div><p>{message}</p></div></div>
        <div className="dialog-footer">
            <button className="secondary" disabled={busy} onClick={onClose}>Cancel</button>
            <button className="danger" disabled={busy} onClick={onConfirm}>{busy ? 'Working' : confirmLabel}</button>
        </div>
    </Modal>;
}

function Modal({title, subtitle, onClose, children, wide = false}: {title: string; subtitle?: string; onClose: () => void; children: React.ReactNode; wide?: boolean}) {
    return <div className="modal-backdrop" onMouseDown={e => e.target === e.currentTarget && onClose()}><div className={`modal ${wide ? 'wide' : ''}`}><div className="modal-header"><div><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div><button className="icon-button" title="Close" onClick={onClose}><X size={18}/></button></div>{children}</div></div>;
}

// BrandMark mirrors build/appicon-source.svg: a bridge (pons) over rising bars.
function BrandMark({size = 22}: {size?: number}) {
    return <svg width={size} height={size} viewBox="0 0 1024 1024" fill="none" aria-hidden="true">
        <path d="M212 520 C 300 280 724 280 812 520" stroke="#FFFFFF" strokeWidth="78" strokeLinecap="round"/>
        <rect x="272" y="640" width="124" height="168" rx="62" fill="#FFFFFF" opacity="0.5"/>
        <rect x="450" y="576" width="124" height="232" rx="62" fill="#FFFFFF" opacity="0.75"/>
        <rect x="628" y="512" width="124" height="296" rx="62" fill="#FFFFFF"/>
        <circle cx="744" cy="204" r="52" fill="#B7F5DE"/>
    </svg>;
}

function Metric({icon: Icon, label, value, tone}: {icon: typeof Activity; label: string; value: string; tone: string}) { return <div className="metric"><span className={`metric-icon ${tone}`}><Icon size={19}/></span><div><small>{label}</small><strong>{value}</strong></div></div>; }
function Status({state = 'stopped', message}: {state?: string; message?: string}) { return <div className="status-cell" title={message || ''}><span className={`status-dot ${state}`}/><div><strong>{stateLabel[state] || 'Idle'}</strong><small>{message || 'Not running'}</small></div></div>; }
function CheckRow({ok, label}: {ok: boolean; label: string}) { return <div className={ok ? 'ok' : ''}>{ok ? <Check size={16}/> : <Square size={16}/>}<span>{label}</span></div>; }
function Field({label, children}: {label: string; children: React.ReactNode}) { return <label className="field"><span>{label}</span>{children}</label>; }
// DecimalInput keeps the raw keystrokes in local state so intermediate values
// like "0." or "0.00" survive editing. A controlled number input that coerces
// on every keystroke snaps "0." back to "0", making decimals impossible to type.
function DecimalInput({value, onChange, ...props}: {value: number; onChange: (value: number) => void; step?: number; min?: number; max?: number; className?: string; placeholder?: string}) {
    const [text, setText] = useState(String(value ?? 0));
    useEffect(() => { if (Number(text) !== (value ?? 0)) setText(String(value ?? 0)); }, [value]);
    return <input type="number" inputMode="decimal" value={text} {...props}
        onChange={e => { setText(e.target.value); const n = Number(e.target.value); if (e.target.value.trim() !== '' && !Number.isNaN(n)) onChange(n); }}
        onBlur={() => { const n = Number(text); const clean = Number.isNaN(n) ? 0 : n; onChange(clean); setText(String(clean)); }}/>;
}
function NumberField({label, value, onChange, ...props}: {label: string; value: number; onChange: (value: number) => void; step?: number; min?: number; max?: number}) { return <Field label={label}><DecimalInput value={value} onChange={onChange} {...props}/></Field>; }
const speedOptions = [{label: 'Extreme · 100ms', value: 100}, {label: 'Fast · 500ms', value: 500}, {label: 'Slow · 1s', value: 1000}, {label: 'Very slow · 1m', value: 60000}];
function SpeedField({label, value, onChange}: {label: string; value: number; onChange: (value: number) => void}) {
    const preset = speedOptions.some(option => option.value === value);
    return <Field label={label}><div className="segmented speed-options">{speedOptions.map(option => <button type="button" key={option.value} className={value === option.value ? 'active' : ''} onClick={() => onChange(option.value)}>{option.label}</button>)}</div>{!preset && <small className="field-note">Legacy custom interval: {value}ms. Choose one of the four presets to change it.</small>}</Field>;
}
function Empty({text, action}: {text: string; action?: React.ReactNode}) { return <div className="empty"><Radio size={20}/><span>{text}</span>{action}</div>; }

function upsert<T extends Record<string, any>>(items: T[], value: T, key: keyof T): T[] {
    const index = items.findIndex(item => item[key] === value[key]);
    if (index < 0) return [...items, value];
    const next = [...items]; next[index] = value; return next;
}

function pageSubtitle(page: Page) {
    return ({overview: 'Live state across all market-making pairs', strategies: 'Configure and run independent token pairs', funding: 'Route native coin through relay batches to many wallets', wallets: 'Encrypted local signing accounts', logs: 'Execution and monitor events', settings: 'Network and integration defaults'})[page];
}

export default App;
