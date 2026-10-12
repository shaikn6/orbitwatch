// Package sgp4 predicts where an Earth-orbiting object is from its published
// mean elements, using the SGP4 model those elements are meant for.
//
// The numerical core in the vendored_*.go files is adapted from
// github.com/joshuaferrara/go-satellite (BSD 2-Clause, see
// LICENSE.go-satellite), itself a port of the reference code published with
// Vallado et al., "Revisiting Spacetrack Report #3" (AIAA 2006-6753). This
// file is the interface the rest of the project uses. It is checked against
// the official verification set in orbit_test.go.
package sgp4

import (
	"fmt"
	"math"
	"time"
)

// Elements are the mean elements of one object, in the units used by OMM
// and two-line element sets.
type Elements struct {
	NoradID      int
	Name         string
	Epoch        time.Time
	MeanMotion   float64 // revolutions per day
	Eccentricity float64
	Inclination  float64 // degrees
	RAAN         float64 // right ascension of the ascending node, degrees
	ArgPerigee   float64 // degrees
	MeanAnomaly  float64 // degrees
	BStar        float64 // drag term, 1/earth radii
}

// Orbit is an initialised propagator for one object. It keeps working state
// between calls, so one Orbit must not be used from two goroutines at once.
type Orbit struct {
	NoradID int
	Name    string
	Epoch   time.Time
	sat     Satellite
}

// julianDate returns the Julian date of t without losing the fraction of a
// second; an epoch that is one second off moves a low orbit by 7 km.
func julianDate(t time.Time) float64 {
	t = t.UTC()
	y, m, d := t.Date()
	whole := JDay(y, int(m), d, 0, 0, 0)
	seconds := float64(t.Hour()*3600+t.Minute()*60+t.Second()) + float64(t.Nanosecond())/1e9
	return whole + seconds/86400
}

// FromElements initialises an orbit. opsMode is 'i' for normal use and 'a'
// to reproduce the legacy behaviour the verification set was made with.
func FromElements(e Elements, opsMode byte) (*Orbit, error) {
	switch {
	case e.MeanMotion <= 0 || math.IsNaN(e.MeanMotion):
		return nil, fmt.Errorf("object %d: mean motion %v is not positive", e.NoradID, e.MeanMotion)
	case e.Eccentricity < 0 || e.Eccentricity >= 1 || math.IsNaN(e.Eccentricity):
		return nil, fmt.Errorf("object %d: eccentricity %v is outside [0, 1)", e.NoradID, e.Eccentricity)
	case e.Epoch.IsZero():
		return nil, fmt.Errorf("object %d: no epoch", e.NoradID)
	}
	for _, v := range []float64{e.Inclination, e.RAAN, e.ArgPerigee, e.MeanAnomaly, e.BStar} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("object %d: an element is not a finite number", e.NoradID)
		}
	}
	o := &Orbit{NoradID: e.NoradID, Name: e.Name, Epoch: e.Epoch.UTC()}
	s := &o.sat
	s.whichconst = getGravConst(GravityWGS72)
	s.satnum = int64(e.NoradID)
	s.bstar = e.BStar
	s.ecco = e.Eccentricity
	s.inclo = e.Inclination * DEG2RAD
	s.nodeo = e.RAAN * DEG2RAD
	s.argpo = e.ArgPerigee * DEG2RAD
	s.mo = e.MeanAnomaly * DEG2RAD
	s.no = e.MeanMotion / XPDOTP
	s.jdsatepoch = julianDate(e.Epoch)
	mode := string(opsMode)
	sgp4init(&mode, s.jdsatepoch-2433281.5, s)
	if s.Error != 0 {
		return nil, fmt.Errorf("object %d: %s", e.NoradID, s.ErrorStr)
	}
	return o, nil
}

// At returns position (km) and velocity (km/s) in the TEME frame, minutes
// after the element epoch. An error means the model has no valid answer for
// that time, most often because the object has decayed.
func (o *Orbit) At(minutes float64) (position, velocity [3]float64, err error) {
	o.sat.Error = 0
	p, v := sgp4(&o.sat, minutes)
	if o.sat.Error != 0 {
		return position, velocity, fmt.Errorf("object %d at %+.1f min: %s", o.NoradID, minutes, o.sat.ErrorStr)
	}
	return [3]float64{p.X, p.Y, p.Z}, [3]float64{v.X, v.Y, v.Z}, nil
}

// AtTime is At for a wall-clock time.
func (o *Orbit) AtTime(t time.Time) (position, velocity [3]float64, err error) {
	return o.At(t.Sub(o.Epoch).Minutes())
}

// PeriodMinutes is the orbital period implied by the mean motion.
func (o *Orbit) PeriodMinutes() float64 { return 2 * math.Pi / o.sat.no }
