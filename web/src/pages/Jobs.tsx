import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import JobRow from '../components/JobRow';

export default function Jobs() {
  const { data: jobs } = useQuery({ queryKey: ['jobs'], queryFn: api.jobs, refetchInterval: 2000 });
  return (
    <div className="space-y-2">
      <h1 className="text-lg font-semibold mb-3">Jobs</h1>
      {jobs?.length ? jobs.map((j) => <JobRow key={j.id} job={j} />) : <p className="text-sm text-gray-500">No jobs.</p>}
    </div>
  );
}
