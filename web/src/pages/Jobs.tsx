import JobRow from '../components/JobRow';
import { useJobs } from '../hooks/useJobs';

export default function Jobs() {
  const { data: jobs } = useJobs();
  return (
    <div className="space-y-2">
      <h1 className="text-lg font-semibold mb-3">Jobs</h1>
      {jobs?.length ? jobs.map((j) => <JobRow key={j.id} job={j} />) : <p className="text-sm text-gray-500">No jobs.</p>}
    </div>
  );
}
