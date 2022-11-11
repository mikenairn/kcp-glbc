package dns

const (
	TargetTypeHost                       = "HOST"
	TargetTypeIP                         = "IP"
	DEFAULT_GEO_ATTRIBUTE_CONTINENT      = "North America"
	DEFAULT_GEO_ATTRIBUTE_CONTINENT_CODE = "NA"
)

type Target struct {
	Cluster          string
	TargetType       string
	Value            string
	TargetAttributes TargetAttributes
}

type TargetAttributes struct {
	Geo GeoAttributes
}

func NewTargetAttributes() *TargetAttributes {
	return &TargetAttributes{Geo: GeoAttributes{
		Continent:     DEFAULT_GEO_ATTRIBUTE_CONTINENT,
		ContinentCode: DEFAULT_GEO_ATTRIBUTE_CONTINENT_CODE,
	}}
}

type GeoAttributes struct {
	Continent     string `json:"continent"`
	ContinentCode string `json:"continent_code"`
}
