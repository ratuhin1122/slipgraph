import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, test, vi } from 'vitest';
import { AuthContext } from './auth-context';
import { Integrations } from './Integrations';
import type { Session } from './api/client';
const id='019a1111-1111-7111-8111-111111111111';
const session:Session={id,user:{id,display_name:'A',email:'a@example.com'},workspace_id:id,workspace_name:'A',role:'admin',expires_at:'2035-01-01T00:00:00Z'};
function mount(role:'admin'|'viewer'='admin'){const cache=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}});return render(<QueryClientProvider client={cache}><AuthContext value={{session:{...session,role},pending:false,error:null,accept:vi.fn()}}><Integrations/></AuthContext></QueryClientProvider>);}
const json=(v:unknown)=>new Response(JSON.stringify(v),{headers:{'Content-Type':'application/json'}});
test('integration status/event text is inert and configuration uses CSRF without tenant identity',async()=>{
 let configured=false;
 vi.mocked(fetch).mockImplementation(async input=>{const r=input instanceof Request?input:new Request(input);const path=new URL(r.url).pathname;
 if(path.endsWith('/csrf'))return json({csrf_token:'x'.repeat(43)});
 if(path.endsWith('/channels'))return json([{id,name:'General'}]);
 if(path.endsWith('/events'))return json([{id,event_type:'<img src=x onerror=alert(1)>',outcome:'queued',received_at:'2030-01-01T00:00:00Z'}]);
 if(r.method==='POST'){expect(r.headers.get('X-CSRF-Token')).toBe('x'.repeat(43));const body:unknown=await r.json();expect(body).toEqual({repository:'octo/repo',channel_id:id,stale_days:7,delay_days:1});configured=true;return json({url:'https://app.example/api/hooks/github/opaque',secret:'s'.repeat(43)});}
 return json({enabled:true,connected:true,repository:'octo/repo',channel_id:id,stale_days:7,delay_days:1});});
 const {container}=mount();await screen.findByText('Connected octo/repo');expect(container.querySelector('img,script,iframe')).toBeNull();fireEvent.click(screen.getByRole('button',{name:'Save and rotate webhook credentials'}));await waitFor(()=>expect(configured).toBe(true));await screen.findByText('One-time webhook credentials');expect(screen.getByLabelText('Secret')).toHaveAttribute('type','password');fireEvent.click(screen.getByRole('button',{name:'Dismiss credentials'}));expect(screen.queryByLabelText('Secret')).toBeNull();
});
test('viewer cannot load integration credentials or logs',()=>{mount('viewer');expect(screen.getByText('Only workspace admins can manage integrations.')).toBeInTheDocument();expect(fetch).not.toHaveBeenCalled();});
