import {useEffect, useMemo, useState} from 'react';
import {
    Activity, AlertTriangle, Check, ChevronDown, ChevronUp, CircleDollarSign,
    Copy, Download, Edit3, Eye, EyeOff, FileCheck2, Gauge, KeyRound, LayoutDashboard,
    ListFilter, Lock, LockOpen, Plus, Radio, RefreshCw, Save, Settings as SettingsIcon,
    ShieldCheck, Square, SquareTerminal, StopCircle, Trash2, Upload, WalletCards, X,
} from 'lucide-react';
import {
    Bootstrap, CreateVault, DeleteStrategy, ExportGMGN, GenerateMnemonic,
    ImportMnemonic, ImportPrivateKeys, LockVault, NewStrategy, PreflightStrategy,
    SaveSettings, SaveStrategy, StartStrategy, StopStrategy, UnlockVault,
} from '../wailsjs/go/main/App';
import {control, vault} from '../wailsjs/go/models';
import {EventsOn} from '../wailsjs/runtime/runtime';
import './App.css';

type Page = 'overview' | 'strategies' | 'wallets' | 'logs' | 'settings';
type Toast = {kind: 'success' | 'error' | 'info'; text: string};

const short = (value = '') => value.length > 15 ? `${value.slice(0, 7)}...${value.slice(-5)}` : value || '-';
const isActive = (state?: string) => ['starting', 'running', 'stopping'].includes(state || '');
const stateLabel: Record<string, string> = {
    starting: 'Starting', running: 'Running', stopping: 'Stopping', stopped: 'Stopped', error: 'Error',
};

function App() {
    const [data, setData] = useState<control.Bootstrap | null>(null);
    const [page, setPage] = useState<Page>('overview');
    const [toast, setToast] = useState<Toast | null>(null);
    const [editing, setEditing] = useState<control.Strategy | null>(null);
    const [liveTarget, setLiveTarget] = useState<control.Strategy | null>(null);
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
            EventsOn('strategy-updated', (strategy: control.Strategy) => setData(prev => prev ? ({...prev, strategies: upsert(prev.strategies || [], strategy, 'id')}) as control.Bootstrap : prev)),
            EventsOn('strategy-deleted', (id: string) => setData(prev => prev ? ({...prev, strategies: (prev.strategies || []).filter(s => s.id !== id)}) as control.Bootstrap : prev)),
            EventsOn('strategy-log', (entry: control.LogEntry) => setData(prev => prev ? ({...prev, logs: [...(prev.logs || []), entry].slice(-800)}) as control.Bootstrap : prev)),
            EventsOn('vault-updated', (state: control.VaultState) => setData(prev => prev ? ({...prev, vault: state}) as control.Bootstrap : prev)),
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

    if (!data) return <div className="boot"><Activity className="spin"/> Loading PonsDesk</div>;

    const nav: {id: Page; label: string; icon: typeof Activity}[] = [
        {id: 'overview', label: 'Overview', icon: LayoutDashboard},
        {id: 'strategies', label: 'Strategies', icon: Radio},
        {id: 'wallets', label: 'Wallet vault', icon: WalletCards},
        {id: 'logs', label: 'Activity logs', icon: SquareTerminal},
        {id: 'settings', label: 'Settings', icon: SettingsIcon},
    ];

    return <div className="shell">
        <aside className="sidebar">
            <div className="brand"><div className="brand-mark"><Activity size={20}/></div><div><strong>PonsDesk</strong><span>Robinhood Chain</span></div></div>
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
                    <button className="icon-button" title="Refresh" onClick={reload}><RefreshCw size={17}/></button>
                    <button className={`vault-chip ${data.vault.unlocked ? 'unlocked' : ''}`} onClick={() => setPage('wallets')}>
                        {data.vault.unlocked ? <LockOpen size={16}/> : <Lock size={16}/>} {data.vault.unlocked ? `${data.vault.wallets?.length || 0} wallets` : 'Vault locked'}
                    </button>
                </div>
            </header>

            <div className="content">
                {page === 'overview' && <Overview data={data} jobs={jobs} onNavigate={setPage} onEdit={setEditing} onStart={setLiveTarget} onStop={stop}/>}
                {page === 'strategies' && <Strategies data={data} jobs={jobs} busy={busy} onAdd={addStrategy} onEdit={setEditing} onStart={setLiveTarget} onStop={stop} onPreflight={preflight} notify={notify}/>}
                {page === 'wallets' && <WalletVault state={data.vault} notify={notify}/>}
                {page === 'logs' && <Logs logs={data.logs || []} strategies={data.strategies || []}/>}
                {page === 'settings' && <SettingsPage initial={data.settings} notify={notify}/>}
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
        {toast && <div className={`toast ${toast.kind}`}>{toast.kind === 'error' ? <AlertTriangle size={17}/> : <Check size={17}/>}<span>{toast.text}</span></div>}
    </div>;
}

function Overview({data, jobs, onNavigate, onEdit, onStart, onStop}: {
    data: control.Bootstrap; jobs: Map<string, control.JobStatus>; onNavigate: (p: Page) => void;
    onEdit: (s: control.Strategy) => void; onStart: (s: control.Strategy) => void; onStop: (id: string) => void;
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
            <StrategyTable strategies={data.strategies || []} jobs={jobs} onEdit={onEdit} onStart={onStart} onStop={onStop}/>
        </section>
        <section className="split-section">
            <div className="section-block compact"><div className="section-heading"><div><h2>Recent activity</h2><p>Latest engine messages across all pairs.</p></div><button className="icon-button" title="Open logs" onClick={() => onNavigate('logs')}><SquareTerminal size={17}/></button></div>
                <div className="mini-log">{(data.logs || []).slice(-7).reverse().map((log, i) => <div key={`${log.at}-${i}`}><time>{new Date(log.at).toLocaleTimeString()}</time><span className={log.level}>{log.level}</span><p>{log.message}</p></div>)}{!data.logs?.length && <Empty text="No engine activity yet"/>}</div>
            </div>
            <div className="section-block compact"><div className="section-heading"><div><h2>Readiness</h2><p>Required local and network state.</p></div></div>
                <div className="checklist"><CheckRow ok={Boolean(data.settings.rpcEndpoint)} label="Robinhood RPC configured"/><CheckRow ok={data.vault.unlocked} label="Wallet vault unlocked"/><CheckRow ok={(data.strategies?.length || 0) > 0} label="At least one strategy saved"/><CheckRow ok={(data.vault.wallets?.length || 0) > 1} label="Maker wallets available"/></div>
            </div>
        </section>
    </>;
}

function Strategies({data, jobs, busy, onAdd, onEdit, onStart, onStop, onPreflight, notify}: {
    data: control.Bootstrap; jobs: Map<string, control.JobStatus>; busy: string; onAdd: () => void;
    onEdit: (s: control.Strategy) => void; onStart: (s: control.Strategy) => void; onStop: (id: string) => void;
    onPreflight: (s: control.Strategy) => void; notify: (k: Toast['kind'], t: string) => void;
}) {
    const remove = async (s: control.Strategy) => {
        if (!window.confirm(`Delete strategy "${s.name}"?`)) return;
        try { await DeleteStrategy(s.id); notify('success', 'Strategy deleted'); }
        catch (e) { notify('error', String(e)); }
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
                return <div className="table-row" key={s.id}>
                    <div className="name-cell"><span className="pair-icon">{(s.token?.symbol || s.name || '?').slice(0, 2).toUpperCase()}</span><div><strong>{s.name}</strong><small>{s.token?.symbol || 'Unlabelled token'}</small></div></div>
                    <div className="mono-cell"><strong>{short(s.tokenAddress)}</strong><small>{short(s.poolAddress)}</small></div>
                    <div><strong>{s.walletIds?.length || 0}</strong><small>1 treasury + {Math.max(0, (s.walletIds?.length || 0) - 1)} makers</small></div>
                    <div><span className="mode-tag">{(s.protocol || 'v1').toUpperCase()} · {s.mode === 'launch' ? 'Launch' : 'Existing'}</span></div>
                    <Status state={job?.state} message={job?.message}/>
                    <div className="row-actions">
                        <button title="Preflight" disabled={active || busy === `preflight:${s.id}`} onClick={() => onPreflight(s)}><FileCheck2 size={16}/></button>
                        {active ? <button className="danger-icon" title="Stop" onClick={() => onStop(s.id)}><StopCircle size={16}/></button> : <button className="start-icon" title="Start live" onClick={() => onStart(s)}><Activity size={16}/></button>}
                        <button title="Export GMGN tags" disabled={!data.vault.unlocked} onClick={() => exportGMGN(s)}><Download size={16}/></button>
                        <button title="Edit" disabled={active} onClick={() => onEdit(s)}><Edit3 size={16}/></button>
                        <button title="Delete" disabled={active} onClick={() => remove(s)}><Trash2 size={16}/></button>
                    </div>
                </div>;
            })}
            {!data.strategies?.length && <Empty text="No strategies configured" action={<button className="secondary" onClick={onAdd}><Plus size={16}/> Create strategy</button>}/>}
        </div>
    </section>;
}

function StrategyTable({strategies, jobs, onEdit, onStart, onStop}: {
    strategies: control.Strategy[]; jobs: Map<string, control.JobStatus>;
    onEdit: (s: control.Strategy) => void; onStart: (s: control.Strategy) => void; onStop: (id: string) => void;
}) {
    return <div className="data-table overview-table"><div className="table-head"><span>Strategy</span><span>Token / pool</span><span>Status</span><span>Action</span></div>
        {strategies.slice(0, 8).map(s => { const job = jobs.get(s.id); const active = isActive(job?.state); return <div className="table-row" key={s.id}>
            <div className="name-cell"><span className="pair-icon">{(s.token?.symbol || s.name).slice(0, 2).toUpperCase()}</span><div><strong>{s.name}</strong><small>{s.walletIds?.length || 0} wallets</small></div></div>
            <div className="mono-cell"><strong>{short(s.tokenAddress)}</strong><small>{short(s.poolAddress)}</small></div><Status state={job?.state} message={job?.message}/>
            <div className="row-actions"><button title="Edit" disabled={active} onClick={() => onEdit(s)}><Edit3 size={16}/></button>{active ? <button className="danger-icon" title="Stop" onClick={() => onStop(s.id)}><StopCircle size={16}/></button> : <button className="start-icon" title="Start live" onClick={() => onStart(s)}><Activity size={16}/></button>}</div>
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
    const set = (key: string, value: any) => setDraft((d: any) => ({...d, [key]: value}));
    const setToken = (key: string, value: any) => setDraft((d: any) => ({...d, token: {...(d.token || {}), [key]: value}}));
    const setSocial = (key: string, value: string) => setDraft((d: any) => ({...d, token: {...(d.token || {}), socials: {...(d.token?.socials || {}), [key]: value}}}));
    const selected: string[] = draft.walletIds || [];
    const toggleWallet = (id: string) => set('walletIds', selected.includes(id) ? selected.filter(x => x !== id) : [...selected, id]);
    const moveWallet = (index: number, direction: -1 | 1) => {
        const next = [...selected]; const target = index + direction; if (target < 0 || target >= next.length) return;
        [next[index], next[target]] = [next[target], next[index]]; set('walletIds', next);
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
                <Field label="Pons protocol"><div className="segmented"><button className={draft.protocol === 'v2' ? 'active' : ''} onClick={() => { set('protocol', 'v2'); set('devBuyEth', 0); }}>v2 bonding curve</button><button className={(draft.protocol || 'v1') === 'v1' ? 'active' : ''} onClick={() => set('protocol', 'v1')}>v1 · V3 pool</button></div></Field>
                <div className="inline-note"><Gauge size={17}/><span>{draft.protocol === 'v2' ? 'v2 is the current launch stack. It trades on the bonding curve and stops safely when the token graduates to Uniswap v4.' : 'v1 launches directly into a Uniswap v3 pool and may require a whitelisted deployer while its public gate is closed.'}</span></div>
                {draft.mode === 'existing' ? <div className="form-grid two"><Field label="Token address"><input className="mono" value={draft.tokenAddress || ''} onChange={e => set('tokenAddress', e.target.value)} placeholder="0x..."/></Field><Field label={draft.protocol === 'v2' ? 'Bonding curve address' : 'V3 pool address'}><input className="mono" value={draft.poolAddress || ''} onChange={e => set('poolAddress', e.target.value)} placeholder="0x..."/></Field></div> : <>
                    <div className="form-grid two"><Field label="Token name"><input value={draft.token?.name || ''} onChange={e => setToken('name', e.target.value)}/></Field><Field label="Symbol"><input value={draft.token?.symbol || ''} onChange={e => setToken('symbol', e.target.value.toUpperCase())}/></Field></div>
                    <Field label="Logo URL"><input value={draft.token?.logo || ''} onChange={e => setToken('logo', e.target.value)} placeholder="https://.../logo.png"/></Field>
                    <Field label="Description"><textarea rows={3} value={draft.token?.description || ''} onChange={e => setToken('description', e.target.value)}/></Field>
                    <div className="form-grid two"><Field label={draft.protocol === 'v2' ? 'Creator fee recipient (optional)' : 'Fee wallet (optional)'}><input className="mono" value={draft.token?.feeWallet || ''} onChange={e => setToken('feeWallet', e.target.value)} placeholder="Defaults to deployer"/></Field>{draft.protocol === 'v2' ? <Field label="Initial buy"><input disabled value="Not available for v2 direct launch"/></Field> : <Field label="Initial buy (ETH)"><input type="number" min="0" step="0.001" value={draft.devBuyEth ?? 0} onChange={e => set('devBuyEth', +e.target.value)}/></Field>}</div>
                    <div className="form-grid two"><Field label="Website"><input value={draft.token?.socials?.website || ''} onChange={e => setSocial('website', e.target.value)}/></Field><Field label="X / Twitter"><input value={draft.token?.socials?.twitter || ''} onChange={e => setSocial('twitter', e.target.value)}/></Field><Field label="Telegram"><input value={draft.token?.socials?.telegram || ''} onChange={e => setSocial('telegram', e.target.value)}/></Field><Field label="Farcaster"><input value={draft.token?.socials?.farcaster || ''} onChange={e => setSocial('farcaster', e.target.value)}/></Field></div>
                </>}
            </div>}
            {tab === 'wallets' && <div className="wallet-assignment">
                <div className="assignment-header"><div><strong>Execution order</strong><p>The first wallet is treasury/deployer; remaining wallets are makers.</p></div></div>
                {!wallets.length && <Empty text="Unlock the vault and import wallets first"/>}
                {selected.map((id, index) => { const w = wallets.find(item => item.id === id); if (!w) return null; return <div className="assigned-wallet" key={id}><span className="role-index">{index + 1}</span><div><strong>{w.label}</strong><small className="mono">{w.address}</small></div><span className={`role-tag ${index === 0 ? 'treasury' : ''}`}>{index === 0 ? 'Treasury' : 'Maker'}</span><button title="Move up" disabled={index === 0} onClick={() => moveWallet(index, -1)}><ChevronUp size={15}/></button><button title="Move down" disabled={index === selected.length - 1} onClick={() => moveWallet(index, 1)}><ChevronDown size={15}/></button><button title="Remove" onClick={() => toggleWallet(id)}><X size={15}/></button></div>; })}
                <div className="available-wallets"><strong>Available wallets</strong>{wallets.filter(w => !selected.includes(w.id)).map(w => <button key={w.id} onClick={() => toggleWallet(w.id)}><Plus size={15}/><span>{w.label}</span><small className="mono">{short(w.address)}</small></button>)}</div>
            </div>}
            {tab === 'execution' && <div className="form-stack">
                <div className="form-grid three"><NumberField label="Buy fraction" value={draft.buyFraction} step={0.01} min={0.01} max={1} onChange={v => set('buyFraction', v)}/><NumberField label="Accumulate interval (ms)" value={draft.accumulateIntervalMs} step={100} min={100} onChange={v => set('accumulateIntervalMs', v)}/><NumberField label="Chip target" value={draft.chipTarget} step={0.05} min={0.05} max={1} onChange={v => set('chipTarget', v)}/><NumberField label="High hold threshold" value={draft.highHold} step={0.05} min={0.05} max={1} onChange={v => set('highHold', v)}/><NumberField label="Oscillation band" value={draft.oscillationBand} step={0.01} min={0.01} max={0.99} onChange={v => set('oscillationBand', v)}/><NumberField label="Sell interval (ms)" value={draft.sellIntervalMs} step={100} min={100} onChange={v => set('sellIntervalMs', v)}/><NumberField label="Sell tranche" value={draft.sellTranche} step={0.05} min={0.05} max={1} onChange={v => set('sellTranche', v)}/><NumberField label="Slippage (bps)" value={draft.slippageBps} step={50} min={0} max={9999} onChange={v => set('slippageBps', v)}/><NumberField label="Priority tip (gwei)" value={draft.priorityTipGwei} step={0.1} min={0} onChange={v => set('priorityTipGwei', v)}/><NumberField label="Gas reserve (ETH)" value={draft.gasReserveEth} step={0.001} min={0} onChange={v => set('gasReserveEth', v)}/></div>
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

function SettingsPage({initial, notify}: {initial: control.Settings; notify: (k: Toast['kind'], t: string) => void}) {
    const [settings, setSettings] = useState<any>(() => ({...initial})); const [showRPC, setShowRPC] = useState(false); const [saving, setSaving] = useState(false);
    const save = async () => { setSaving(true); try { await SaveSettings(new control.Settings(settings)); notify('success', 'Settings saved'); } catch (e) { notify('error', String(e)); } finally { setSaving(false); } };
    return <section className="settings-layout"><div className="section-block"><div className="section-heading"><div><h2>Network</h2><p>Shared defaults for all configured pairs.</p></div></div><Field label="Robinhood Chain RPC"><div className="password-input"><input className="mono" type={showRPC ? 'text' : 'password'} value={settings.rpcEndpoint || ''} onChange={e => setSettings({...settings, rpcEndpoint: e.target.value})} placeholder="wss://..."/><button title={showRPC ? 'Hide endpoint' : 'Reveal endpoint'} onClick={() => setShowRPC(!showRPC)}>{showRPC ? <EyeOff size={17}/> : <Eye size={17}/>}</button></div></Field><div className="inline-note"><Gauge size={17}/><span>WebSocket endpoints enable event-driven pool monitoring. HTTPS falls back to polling.</span></div></div>
        <div className="section-block"><div className="section-heading"><div><h2>GMGN viewer</h2><p>Optional account used when manually importing generated wallet tags.</p></div></div><Field label="Viewer wallet address"><input className="mono" value={settings.gmgnViewerWallet || ''} onChange={e => setSettings({...settings, gmgnViewerWallet: e.target.value})} placeholder="0x..."/></Field></div>
        <div className="settings-actions"><button className="primary" disabled={saving} onClick={save}><Save size={16}/>{saving ? 'Saving' : 'Save settings'}</button></div>
    </section>;
}

function LiveDialog({strategy, onClose, onConfirm}: {strategy: control.Strategy; onClose: () => void; onConfirm: () => void}) {
    const [phrase, setPhrase] = useState('');
    return <Modal title={strategy.mode === 'launch' ? 'Launch token and start market maker' : 'Start live market maker'} subtitle={strategy.name} onClose={onClose}><div className="risk-box"><AlertTriangle size={20}/><div><strong>This sends real transactions</strong><p>Selected wallets can spend ETH, pay gas, buy, approve, and sell tokens according to this strategy.</p></div></div><Field label="Type LIVE to confirm"><input autoFocus value={phrase} onChange={e => setPhrase(e.target.value.toUpperCase())}/></Field><div className="dialog-footer"><button className="secondary" onClick={onClose}>Cancel</button><button className="danger" disabled={phrase !== 'LIVE'} onClick={onConfirm}><Activity size={16}/>{strategy.mode === 'launch' ? 'Launch and run' : 'Start live'}</button></div></Modal>;
}

function Modal({title, subtitle, onClose, children, wide = false}: {title: string; subtitle?: string; onClose: () => void; children: React.ReactNode; wide?: boolean}) {
    return <div className="modal-backdrop" onMouseDown={e => e.target === e.currentTarget && onClose()}><div className={`modal ${wide ? 'wide' : ''}`}><div className="modal-header"><div><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div><button className="icon-button" title="Close" onClick={onClose}><X size={18}/></button></div>{children}</div></div>;
}

function Metric({icon: Icon, label, value, tone}: {icon: typeof Activity; label: string; value: string; tone: string}) { return <div className="metric"><span className={`metric-icon ${tone}`}><Icon size={19}/></span><div><small>{label}</small><strong>{value}</strong></div></div>; }
function Status({state = 'stopped', message}: {state?: string; message?: string}) { return <div className="status-cell" title={message || ''}><span className={`status-dot ${state}`}/><div><strong>{stateLabel[state] || 'Idle'}</strong><small>{message || 'Not running'}</small></div></div>; }
function CheckRow({ok, label}: {ok: boolean; label: string}) { return <div className={ok ? 'ok' : ''}>{ok ? <Check size={16}/> : <Square size={16}/>}<span>{label}</span></div>; }
function Field({label, children}: {label: string; children: React.ReactNode}) { return <label className="field"><span>{label}</span>{children}</label>; }
function NumberField({label, value, onChange, ...props}: {label: string; value: number; onChange: (value: number) => void; step?: number; min?: number; max?: number}) { return <Field label={label}><input type="number" value={value ?? 0} onChange={e => onChange(Number(e.target.value))} {...props}/></Field>; }
function Empty({text, action}: {text: string; action?: React.ReactNode}) { return <div className="empty"><Radio size={20}/><span>{text}</span>{action}</div>; }

function upsert<T extends Record<string, any>>(items: T[], value: T, key: keyof T): T[] {
    const index = items.findIndex(item => item[key] === value[key]);
    if (index < 0) return [...items, value];
    const next = [...items]; next[index] = value; return next;
}

function pageSubtitle(page: Page) {
    return ({overview: 'Live state across all market-making pairs', strategies: 'Configure and run independent token pairs', wallets: 'Encrypted local signing accounts', logs: 'Execution and monitor events', settings: 'Network and integration defaults'})[page];
}

export default App;
