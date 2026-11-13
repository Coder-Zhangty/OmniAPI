"""Durable incident deduplication, recovery and bounded delivery backoff."""
import time


class Alerts:
    def __init__(self,store,publish,clock=time.time):
        self.store,self.publish,self.clock=store,publish,clock

    def check(self,key,node,bad,title,detail,duration=0,recovery_samples=3,suppress=False):
        now=self.clock()
        state=self.store.state('incident:'+key,{'bad_since':None,'bad_samples':0,'good_samples':0,'open':False,'last_sent':0,'next_attempt':0,'failures':0})
        if bad:
            state['recovery_pending']=False
            state['good_samples']=0;state['bad_samples']+=1
            if state['bad_since'] is None:state['bad_since']=now
            if suppress:
                self.store.set_state('incident:'+key,state);return
            confirmed=(now-state['bad_since']>=duration) if duration else state['bad_samples']>=3
            if confirmed and not state['open']:
                state['open']=True
                self.store.event('alert',node,{'title':title,'detail':detail,'key':key})
            if state['open'] and now>=state['next_attempt'] and (not state['last_sent'] or now-state['last_sent']>=1800):
                self._send(state,node,title,detail,now)
        else:
            state['bad_since']=None;state['bad_samples']=0;state['good_samples']+=1
            if state['open'] and state['good_samples']>=recovery_samples:
                if not state.get('recovery_pending'):
                    state['recovery_pending']=True
                    self.store.event('recovery',node,{'title':title,'detail':'Recovered','key':key})
                    state['next_attempt']=0
                if now>=state['next_attempt'] and self._send(state,node,'恢复：'+title,detail,now):
                    state={'bad_since':None,'bad_samples':0,'good_samples':0,'open':False,'last_sent':0,'next_attempt':0,'failures':0}
        self.store.set_state('incident:'+key,state)

    def _send(self,state,node,title,detail,now):
        result=self.publish(title,detail,node)
        ok=bool(result.get('ok'))
        self.store.event('notification',node,{'title':title,'ok':ok,'error':str(result.get('error') or '')[:160]})
        if ok:
            state['last_sent']=now;state['failures']=0;state['next_attempt']=now+1800
        else:
            state['failures']+=1;state['next_attempt']=now+min(900,30*2**min(state['failures']-1,5))
        return ok
