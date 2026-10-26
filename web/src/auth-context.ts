import { createContext, useContext } from 'react';
import type { Session } from './api/client';
export type AuthState = { session: Session | null; pending: boolean; error: string | null; accept: (session: Session) => void };
export const AuthContext = createContext<AuthState | null>(null);
export function useAuth() { const value = useContext(AuthContext); if (!value) throw new Error('Authentication provider missing'); return value; }
export function useSession(): Session { const { session } = useAuth(); if (!session) throw new Error('Authenticated route required'); return session; }
