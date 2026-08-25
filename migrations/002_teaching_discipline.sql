-- The teaching catalogue gained a discipline tag so apprentices can filter
-- workshops by craft focus. Existing rows keep the neutral default.
ALTER TABLE workshops ADD COLUMN discipline TEXT NOT NULL DEFAULT 'storyboard';

CREATE INDEX idx_workshops_discipline ON workshops (studio_id, discipline, state);

-- Practice submissions are versioned per apprentice attempt. The attempt number
-- lets the director see how many revisions a workshop needed.
ALTER TABLE practice_submissions ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1;

CREATE INDEX idx_submissions_enrollment ON practice_submissions (enrollment_id, attempt);
