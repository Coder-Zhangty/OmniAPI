export interface SecureStore { get(key:string):Promise<string|null>; set(key:string,value:string):Promise<void>; delete(key:string):Promise<void>; }
export interface Envelope { v:1; nonce:string; ciphertext:string; }
export interface Keyring { v:1; uid:string; salt:string; kdf:{name:'argon2id';m:65536;t:3;p:1}; password_wrap:Envelope; recovery_wrap:Envelope; }
export interface Revision { object:string; revision:string; parent:string|null; deleted:boolean; envelope:Envelope; }
export interface SQLTransaction {run(sql:string,params:unknown[]):Promise<unknown>;get(sql:string,params:unknown[]):Promise<any>;all(sql:string,params:unknown[]):Promise<any[]>;}
export interface JournalDriver extends SQLTransaction {transaction<T>(action:(tx:SQLTransaction)=>Promise<T>):Promise<T>;}
export class EncryptedJournal {
 constructor(driver:JournalDriver,user:string|number);
 initialize():Promise<void>;enqueue(record:Revision):Promise<void>;outbox(limit?:number):Promise<Revision[]>;
 acknowledge(revision:string,conflict:boolean):Promise<void>;cursor():Promise<number>;
 applyBatch(values:Array<{record:Revision & {cursor:number;purged?:boolean};value:unknown}>,cursor:number):Promise<void>;
 heads(object:string):Promise<any[]>;clear():Promise<void>;
 objects(limit?:number,offset?:number):Promise<any[]>;
}
export class AccountClient {
 constructor(options:{secureStore:SecureStore;launch:(url:string)=>Promise<unknown>;platform:'windows'|'android';fetch?:typeof fetch});
 session:{user_id:number;auth:{access_token:string;access_expires_at:number;session:{sid:string}};refresh_token:string}|null;
 load():Promise<number|null>; device():Promise<string>; login(signal?:AbortSignal):Promise<number>;
 access():Promise<string>; request<T=unknown>(path:string,body?:unknown):Promise<T>;
 syncToken(device:string):Promise<{token:string;expires_at:number;url:string}>;
 webLogin():Promise<{request:string;verifier:string}>; logout():Promise<void>;
}
export function newId():string;
export function b64(value:Uint8Array):string;
export function unb64(value:string):Uint8Array;
export function createKeyring(user:string|number,password:string):Promise<{master:Uint8Array;recovery:string;keyring:Keyring}>;
export function unlockPassword(ring:Keyring,password:string,user:string|number):Promise<Uint8Array>;
export function unlockRecovery(ring:Keyring,recovery:string,user:string|number):Uint8Array;
export function changePassword(ring:Keyring,master:Uint8Array,password:string):Promise<Keyring>;
export function sealRecord(master:Uint8Array,user:string|number,object:string,revision:string,parent:string|null,deleted:boolean,value:unknown):Envelope;
export function openRecord(master:Uint8Array,user:string|number,record:Revision):unknown;
export interface Release {platform:string;version:string;sha256:string;bytes:number;primary:string;mirror:string;source:string;accepted:true;}
export function verifyManifest(value:unknown,publicKey:string,platform:string,current:string):Release|null;
export function verifyPackage(bytes:Uint8Array,release:Release):true;
export function verifyPackageStream(chunks:AsyncIterable<Uint8Array>,release:Release):Promise<true>;
export interface KnowledgeIndex {v:1;id:string;dimensions:number;chunks:Array<{id:string;text:string;source?:string;vector:number[]}>;}
export function validateKnowledgeIndex(index:unknown):KnowledgeIndex;
export function searchKnowledge(index:KnowledgeIndex,vector:number[],limit?:number):Array<{id:string;text:string;source?:string;score:number}>;
export function requireToolApproval(tool:{operationId?:string;classification:string;withinGrantedScope:boolean},authorization?:{operationId:string;consumed?:boolean}):void;
export class SyncEngine {
 constructor(options:{user:string|number;device:string;adapter:unknown;token:(device:string)=>Promise<{url:string;token:string}>;fetch?:typeof fetch});
 unlock(key:Uint8Array):void;lock():void;queue(object:string,parent:string|null,value:unknown,deleted?:boolean):Promise<string>;run():Promise<void>;
}
export class SyncWorkspace {
 readonly ring:Keyring|null;readonly master:Uint8Array|null;
 constructor(options:{user:string|number;device:string;journal:EncryptedJournal;token:(device:string)=>Promise<{url:string;token:string}>;secureStore:SecureStore;fetch?:typeof fetch});
 initialize():Promise<{configured:boolean;unlocked:boolean}>;
 create(password:string,remember?:boolean):Promise<string>;unlock(secret:string,mode?:'password'|'recovery',remember?:boolean):Promise<void>;
 changePassword(password:string,remember?:boolean):Promise<void>;lock(forget?:boolean):Promise<void>;
 objectId(kind:string,localId:string):string;
 put(kind:string,localId:string,value:unknown,parent?:string|null,deleted?:boolean):Promise<string>;
 list(options?:{limit?:number;offset?:number;deleted?:boolean}):Promise<Array<{object:string;revision:string;parent:string|null;deleted:boolean;pending:boolean;value:unknown}>>;
 sync():Promise<void>;status():Promise<unknown>;
}
