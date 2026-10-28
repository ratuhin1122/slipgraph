import { Navigate, Route, Routes } from 'react-router-dom';
import { AuthProvider, Login, Protected, Shell } from './Auth';
import { Members, AcceptInvite } from './Members';
import { Integrations } from './Integrations';
import { Projects } from './Projects';
import { Meetings, MeetingPage } from './Meetings';
import { Plans } from './AgentFeatures';
import { Proposals } from './Proposals';
import { Chat } from './Chat';
import { ProjectPage } from './ProjectPage';
export default function App() { return <AuthProvider><Routes><Route path="/login" element={<Login key="login" />} /><Route path="/signup" element={<Login key="signup" />} /><Route path="/" element={<Navigate to="/projects" replace />} /><Route path="/projects" element={<Protected><Shell><Projects /></Shell></Protected>} /><Route path="/projects/:projectId" element={<Protected><Shell><ProjectPage /></Shell></Protected>} /><Route path="/meetings" element={<Protected><Shell><Meetings /></Shell></Protected>} /><Route path="/meetings/:meetingId" element={<Protected><Shell><MeetingPage /></Shell></Protected>} /><Route path="/plans" element={<Protected><Shell><Plans /></Shell></Protected>} /><Route path="/proposals" element={<Protected><Shell><Proposals /></Shell></Protected>} /><Route path="/chat" element={<Protected><Shell><Chat /></Shell></Protected>} /><Route path="/chat/:channelId" element={<Protected><Shell><Chat /></Shell></Protected>} /><Route path="/settings/integrations" element={<Protected><Shell><Integrations /></Shell></Protected>} /><Route path="/members" element={<Protected><Shell><Members /></Shell></Protected>} /><Route path="/invite" element={<Protected><Shell><AcceptInvite /></Shell></Protected>} /><Route path="*" element={<Navigate to="/projects" replace />} /></Routes></AuthProvider>; }
