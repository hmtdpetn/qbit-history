package model

// Sample is one logical observation of a torrent (or an instance's global
// counters). Byte values are integers; JSON uses decimal strings so that
// JavaScript never silently loses precision above 2^53.
type Sample struct {
	At         int64  `json:"at"`
	Epoch      uint64 `json:"epoch"`
	Seq        uint64 `json:"seq"`
	Up         int64  `json:"up,string"`
	Down       int64  `json:"down,string"`
	Uploaded   int64  `json:"uploaded,string"`
	Downloaded int64  `json:"downloaded,string"`
	Valid      uint8  `json:"valid"`
	Quality    uint16 `json:"quality"`
	StepMS     int64  `json:"step_ms"`
}

const (
	ValidUp uint8 = 1 << iota
	ValidDown
	ValidUploaded
	ValidDownloaded
)
const (
	GapBefore uint16 = 1 << iota
	CounterReset
	ClockChange
	Partial
)
const CoreValid = ValidUp | ValidDown | ValidUploaded | ValidDownloaded

// Continuous reports whether s directly follows p in the same collection epoch
// without a skipped round, a gap flag or an implausible time jump.
func (s Sample) Continuous(p Sample) bool {
	return s.Epoch == p.Epoch && s.Seq == p.Seq+1 && s.At > p.At && s.At-p.At <= s.StepMS*2 && s.Quality&(GapBefore|ClockChange) == 0
}

type Instance struct {
	ID               string `json:"instance_id"`
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	Username         string `json:"username"`
	Secret           []byte `json:"-"`
	PollEnabled      bool   `json:"poll_enabled"`
	CredentialsSaved bool   `json:"credentials_saved"`
}
type Torrent struct {
	ID            int64   `json:"torrent_id"`
	InstanceID    string  `json:"instance_id"`
	Key           string  `json:"qb_key"`
	Generation    int     `json:"generation"`
	Name          string  `json:"name"`
	Category      string  `json:"category"`
	Tags          string  `json:"tags"`
	Size          int64   `json:"size,string"`
	State         string  `json:"state"`
	Progress      float64 `json:"progress"`
	Ratio         float64 `json:"ratio"`
	Peers         int     `json:"peers"`
	Seeds         int     `json:"seeds"`
	Availability  float64 `json:"availability"`
	Added         int64   `json:"added_on"`
	Completed     int64   `json:"completion_on"`
	FirstSeen     int64   `json:"first_seen_at"`
	SeriesID      int64   `json:"series_id"`
	Sample        Sample  `json:"current"`
	Candidate     bool    `json:"deletion_candidate"`
	LastUpload    int64   `json:"last_upload_at"`
	Upload1h      *string `json:"upload_1h"`
	Upload24h     *string `json:"upload_24h"`
	StatsCoverage float64 `json:"stats_coverage"`
	StatsAt       int64   `json:"stats_at"`
}
type Batch struct {
	SeriesID int64
	Samples  []Sample
}
type Settings struct {
	RetentionDays int   `json:"retention_days"`
	Interval      int   `json:"interval_seconds"`
	Budget        int64 `json:"data_budget_bytes"`
	TimeoutMS     int   `json:"timeout_ms"`
	ResponseBytes int64 `json:"response_limit_bytes"`
}

func DefaultSettings() Settings { return Settings{7, 1, 2 << 30, 2000, 8 << 20} }

type Event struct {
	InstanceID string `json:"instance_id"`
	SeriesID   int64  `json:"series_id"`
	At         int64  `json:"at"`
	End        int64  `json:"end"`
	Kind       string `json:"kind"`
	Detail     string `json:"detail"`
}
