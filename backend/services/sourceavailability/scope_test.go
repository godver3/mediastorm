package sourceavailability

import (
	"testing"

	"novastream/models"
)

type scopeProfiles struct {
	value *models.UserSettings
	calls int
}

func (p *scopeProfiles) Get(string) (*models.UserSettings, error) { p.calls++; return p.value, nil }

type scopeDevices struct {
	value *models.ClientFilterSettings
	calls int
}

func (d *scopeDevices) Get(string, string) (*models.ClientFilterSettings, error) {
	d.calls++
	return d.value, nil
}

type scopeContent struct {
	value []models.ContentPreference
	calls int
}

func (c *scopeContent) List(string) ([]models.ContentPreference, error) {
	c.calls++
	return c.value, nil
}

func TestAvailabilityScopeChangesWithInheritedDefaultsDeviceAndContent(t *testing.T) {
	_, _, manager := availabilityFixture(t)
	p := &scopeProfiles{value: &models.UserSettings{}}
	d := &scopeDevices{value: &models.ClientFilterSettings{}}
	c := &scopeContent{}
	factory := NewScopeFactory(manager, p, d, c, func(id string) bool { return id == "stefan" })
	resolve, err := factory("stefan", "tv")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := resolve("a")
	for range 100 {
		_, _ = resolve("a")
	}
	if p.calls != 1 || d.calls != 1 || c.calls != 1 {
		t.Fatal("per-card datastore queries")
	}
	cfg, _ := manager.Load()
	cfg.Filtering.MaxResolution = "1080p"
	if err := manager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	resolve, _ = factory("stefan", "tv")
	inherited, _ := resolve("a")
	if inherited.Key == before.Key {
		t.Fatal("inherited global change reused status")
	}
	deu := "deu"
	d.value.PreferredAudioLanguage = &deu
	resolve, _ = factory("stefan", "tv")
	device, _ := resolve("a")
	if device.Key == inherited.Key || device.AudioLanguage == "" {
		t.Fatal("device language did not invalidate")
	}
	manifest, _ := resolve("")
	c.value = []models.ContentPreference{{ContentID: "a", AudioLanguage: "eng"}}
	resolve, _ = factory("stefan", "tv")
	content, _ := resolve("a")
	afterManifest, _ := resolve("")
	if content.Key == device.Key || content.AudioLanguage != "eng" || afterManifest.Key == manifest.Key {
		t.Fatal("title override or manifest failed")
	}
	if _, err := factory("bernd", "tv"); err == nil {
		t.Fatal("deleted/inaccessible profile resolved")
	}
}
