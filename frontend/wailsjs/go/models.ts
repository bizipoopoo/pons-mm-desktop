export namespace control {
	
	export class FundingTask {
	    id: string;
	    kind: string;
	    targets: string[];
	    state: string;
	    message: string;
	    hopsDone: number;
	    hopsTotal: number;
	    transfersDone: number;
	    transfersTotal: number;
	    createdAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new FundingTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.targets = source["targets"];
	        this.state = source["state"];
	        this.message = source["message"];
	        this.hopsDone = source["hopsDone"];
	        this.hopsTotal = source["hopsTotal"];
	        this.transfersDone = source["transfersDone"];
	        this.transfersTotal = source["transfersTotal"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class FundingWallet {
	    id: string;
	    address: string;
	
	    static createFrom(source: any = {}) {
	        return new FundingWallet(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.address = source["address"];
	    }
	}
	export class FundingConfig {
	    depositCold?: FundingWallet;
	    depositRelays?: FundingWallet[];
	    withdrawRelays?: FundingWallet[];
	    withdrawCold?: string;
	
	    static createFrom(source: any = {}) {
	        return new FundingConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.depositCold = this.convertValues(source["depositCold"], FundingWallet);
	        this.depositRelays = this.convertValues(source["depositRelays"], FundingWallet);
	        this.withdrawRelays = this.convertValues(source["withdrawRelays"], FundingWallet);
	        this.withdrawCold = source["withdrawCold"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FundingState {
	    config: FundingConfig;
	    tasks: FundingTask[];
	
	    static createFrom(source: any = {}) {
	        return new FundingState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.config = this.convertValues(source["config"], FundingConfig);
	        this.tasks = this.convertValues(source["tasks"], FundingTask);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class InitStatus {
	    checked: boolean;
	    ok: boolean;
	    balanceEth: string;
	    message: string;
	    checkedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new InitStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checked = source["checked"];
	        this.ok = source["ok"];
	        this.balanceEth = source["balanceEth"];
	        this.message = source["message"];
	        this.checkedAt = source["checkedAt"];
	    }
	}
	export class VaultState {
	    exists: boolean;
	    unlocked: boolean;
	    wallets: vault.Summary[];
	
	    static createFrom(source: any = {}) {
	        return new VaultState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.exists = source["exists"];
	        this.unlocked = source["unlocked"];
	        this.wallets = this.convertValues(source["wallets"], vault.Summary);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogEntry {
	    at: string;
	    strategyId: string;
	    level: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new LogEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at = source["at"];
	        this.strategyId = source["strategyId"];
	        this.level = source["level"];
	        this.message = source["message"];
	    }
	}
	export class JobStats {
	    buyCount: number;
	    sellCount: number;
	    ethSpent: string;
	    ethReceived: string;
	    tokensSold: string;
	    totalCost: string;
	    startBalance: string;
	    endBalance: string;
	    profit: string;
	
	    static createFrom(source: any = {}) {
	        return new JobStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.buyCount = source["buyCount"];
	        this.sellCount = source["sellCount"];
	        this.ethSpent = source["ethSpent"];
	        this.ethReceived = source["ethReceived"];
	        this.tokensSold = source["tokensSold"];
	        this.totalCost = source["totalCost"];
	        this.startBalance = source["startBalance"];
	        this.endBalance = source["endBalance"];
	        this.profit = source["profit"];
	    }
	}
	export class JobStatus {
	    strategyId: string;
	    state: string;
	    message: string;
	    startedAt: string;
	    token: string;
	    pool: string;
	    lastUpdated: string;
	    stats?: JobStats;
	
	    static createFrom(source: any = {}) {
	        return new JobStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.strategyId = source["strategyId"];
	        this.state = source["state"];
	        this.message = source["message"];
	        this.startedAt = source["startedAt"];
	        this.token = source["token"];
	        this.pool = source["pool"];
	        this.lastUpdated = source["lastUpdated"];
	        this.stats = this.convertValues(source["stats"], JobStats);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Socials {
	    twitter: string;
	    telegram: string;
	    discord: string;
	    website: string;
	    farcaster: string;
	
	    static createFrom(source: any = {}) {
	        return new Socials(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.twitter = source["twitter"];
	        this.telegram = source["telegram"];
	        this.discord = source["discord"];
	        this.website = source["website"];
	        this.farcaster = source["farcaster"];
	    }
	}
	export class TokenSpec {
	    name: string;
	    symbol: string;
	    logo: string;
	    description: string;
	    feeWallet: string;
	    socials: Socials;
	
	    static createFrom(source: any = {}) {
	        return new TokenSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.symbol = source["symbol"];
	        this.logo = source["logo"];
	        this.description = source["description"];
	        this.feeWallet = source["feeWallet"];
	        this.socials = this.convertValues(source["socials"], Socials);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Strategy {
	    id: string;
	    name: string;
	    mode: string;
	    protocol: string;
	    enabled: boolean;
	    tokenAddress: string;
	    poolAddress: string;
	    walletIds: string[];
	    token: TokenSpec;
	    launchConfigId: number;
	    dexId: number;
	    devBuyEth: number;
	    bundleMode: string;
	    bundleMaxBlocks: number;
	    buyFraction: number;
	    accumulateIntervalMs: number;
	    concurrentBuys: boolean;
	    chipTarget: number;
	    graduate: boolean;
	    sellIntervalMs: number;
	    sequentialSells: boolean;
	    retailResponseMode: string;
	    retailTargetRatio: number;
	    slippageBps: number;
	    priorityTipGwei: number;
	    gasReserveEth: number;
	    createdAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new Strategy(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.mode = source["mode"];
	        this.protocol = source["protocol"];
	        this.enabled = source["enabled"];
	        this.tokenAddress = source["tokenAddress"];
	        this.poolAddress = source["poolAddress"];
	        this.walletIds = source["walletIds"];
	        this.token = this.convertValues(source["token"], TokenSpec);
	        this.launchConfigId = source["launchConfigId"];
	        this.dexId = source["dexId"];
	        this.devBuyEth = source["devBuyEth"];
	        this.bundleMode = source["bundleMode"];
	        this.bundleMaxBlocks = source["bundleMaxBlocks"];
	        this.buyFraction = source["buyFraction"];
	        this.accumulateIntervalMs = source["accumulateIntervalMs"];
	        this.concurrentBuys = source["concurrentBuys"];
	        this.chipTarget = source["chipTarget"];
	        this.graduate = source["graduate"];
	        this.sellIntervalMs = source["sellIntervalMs"];
	        this.sequentialSells = source["sequentialSells"];
	        this.retailResponseMode = source["retailResponseMode"];
	        this.retailTargetRatio = source["retailTargetRatio"];
	        this.slippageBps = source["slippageBps"];
	        this.priorityTipGwei = source["priorityTipGwei"];
	        this.gasReserveEth = source["gasReserveEth"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Settings {
	    rpcEndpoint: string;
	    gmgnViewerWallet: string;
	    mmRouter: string;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rpcEndpoint = source["rpcEndpoint"];
	        this.gmgnViewerWallet = source["gmgnViewerWallet"];
	        this.mmRouter = source["mmRouter"];
	    }
	}
	export class Bootstrap {
	    settings: Settings;
	    strategies: Strategy[];
	    jobs: JobStatus[];
	    logs: LogEntry[];
	    vault: VaultState;
	    init: InitStatus;
	    funding: FundingState;
	
	    static createFrom(source: any = {}) {
	        return new Bootstrap(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings = this.convertValues(source["settings"], Settings);
	        this.strategies = this.convertValues(source["strategies"], Strategy);
	        this.jobs = this.convertValues(source["jobs"], JobStatus);
	        this.logs = this.convertValues(source["logs"], LogEntry);
	        this.vault = this.convertValues(source["vault"], VaultState);
	        this.init = this.convertValues(source["init"], InitStatus);
	        this.funding = this.convertValues(source["funding"], FundingState);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	
	
	
	export class LaunchPreset {
	    tokenAddress: string;
	    curveAddress: string;
	    block: number;
	    name: string;
	    symbol: string;
	    logo: string;
	    description: string;
	    socials: Socials;
	
	    static createFrom(source: any = {}) {
	        return new LaunchPreset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tokenAddress = source["tokenAddress"];
	        this.curveAddress = source["curveAddress"];
	        this.block = source["block"];
	        this.name = source["name"];
	        this.symbol = source["symbol"];
	        this.logo = source["logo"];
	        this.description = source["description"];
	        this.socials = this.convertValues(source["socials"], Socials);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	

}

export namespace vault {
	
	export class Summary {
	    id: string;
	    address: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new Summary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.address = source["address"];
	        this.label = source["label"];
	    }
	}

}

