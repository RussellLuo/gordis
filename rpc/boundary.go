package gordisrpc

type Contract struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Direction string `json:"direction"`
}

func DescribeBoundary(b Boundary) ([]Contract, error) {
	ds, e := declarations(b)
	if e != nil {
		return nil, e
	}
	out := make([]Contract, 0, len(ds))
	for _, d := range ds {
		out = append(out, Contract{d.Name, d.Version, d.Direction})
	}
	return out, nil
}
