import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import Layout from './components/Layout';
import Dashboard from './pages/Dashboard';
import Browse from './pages/Browse';
import Photos from './pages/Photos';
import Jobs from './pages/Jobs';
import JobDetail from './pages/JobDetail';
import Export from './pages/Export';
import Calibrate from './pages/Calibrate';

const queryClient = new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: false } } });

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/" element={<Dashboard />} />
            <Route path="/browse/*" element={<Browse />} />
            <Route path="/photos" element={<Photos />} />
            <Route path="/jobs" element={<Jobs />} />
            <Route path="/jobs/:id" element={<JobDetail />} />
            <Route path="/export" element={<Export />} />
            <Route path="/calibrate" element={<Calibrate />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
