import { Navigate, Route, Routes } from 'react-router-dom';
import { AuthProvider, Login, Protected, Shell } from './Auth';
import { Members, AcceptInvite } from './Members';
import { Projects } from './Projects';
import { Chat } from './Chat';
import { ProjectPage } from './ProjectPage';
export default function App() { return <AuthProvider><Routes><Route path="/login" element={<Login key="login" />} /><Route path="/signup" element={<Login key="signup" />} /><Route path="/" element={<Navigate to="/projects" replace />} /><Route path="/projects" element={<Protected><Shell><Projects /></Shell></Protected>} /><Route path="/projects/:projectId" element={<Protected><Shell><ProjectPage /></Shell></Protected>} /><Route path="/chat" element={<Protected><Shell><Chat /></Shell></Protected>} /><Route path="/chat/:channelId" element={<Protected><Shell><Chat /></Shell></Protected>} /><Route path="/members" element={<Protected><Shell><Members /></Shell></Protected>} /><Route path="/invite" element={<Protected><Shell><AcceptInvite /></Shell></Protected>} /><Route path="*" element={<Navigate to="/projects" replace />} /></Routes></AuthProvider>; }
