package appstore

import "testing"

func sampleTemplate() AppTemplate {
	return AppTemplate{
		ID:       "jellyfin",
		Name:     "Jellyfin",
		Category: "media",
		Services: []ServiceTemplate{
			{
				Name:  "app",
				Image: "jellyfin/jellyfin:latest",
				Env: []EnvVar{
					{Key: "PUID", Default: "1000"},
					{Key: "TZ", Required: true},
				},
				Ports:   []PortMapping{{ContainerPort: 8096, HostPort: 8096}},
				Volumes: []VolumeMapping{{ContainerPath: "/media"}},
			},
		},
	}
}

func TestAppTemplate_Validate_OK(t *testing.T) {
	if err := sampleTemplate().Validate(); err != nil {
		t.Fatalf("expected valid template, got error: %v", err)
	}
}

func TestAppTemplate_Validate_RequiresID(t *testing.T) {
	tmpl := sampleTemplate()
	tmpl.ID = ""
	if err := tmpl.Validate(); err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestAppTemplate_Validate_RequiresAtLeastOneService(t *testing.T) {
	tmpl := sampleTemplate()
	tmpl.Services = nil
	if err := tmpl.Validate(); err == nil {
		t.Fatal("expected error for no services")
	}
}

func TestAppTemplate_Validate_RejectsDuplicateServiceNames(t *testing.T) {
	tmpl := sampleTemplate()
	tmpl.Services = append(tmpl.Services, tmpl.Services[0])
	if err := tmpl.Validate(); err == nil {
		t.Fatal("expected error for duplicate service names")
	}
}

func TestAppTemplate_Validate_RejectsRequiredWithDefault(t *testing.T) {
	tmpl := sampleTemplate()
	tmpl.Services[0].Env = []EnvVar{{Key: "X", Required: true, Default: "y"}}
	if err := tmpl.Validate(); err == nil {
		t.Fatal("expected error for env var that is both required and has a default")
	}
}

func TestResolveEnv_UsesDefaultsAndUserValues(t *testing.T) {
	svc := sampleTemplate().Services[0]

	env, err := ResolveEnv(svc, map[string]string{"TZ": "Asia/Taipei"})
	if err != nil {
		t.Fatalf("ResolveEnv returned error: %v", err)
	}

	want := map[string]bool{"PUID=1000": false, "TZ=Asia/Taipei": false}
	for _, kv := range env {
		if _, ok := want[kv]; ok {
			want[kv] = true
		} else {
			t.Errorf("unexpected env entry: %q", kv)
		}
	}
	for kv, found := range want {
		if !found {
			t.Errorf("expected env entry %q not found in %v", kv, env)
		}
	}
}

func TestResolveEnv_MissingRequiredValue(t *testing.T) {
	svc := sampleTemplate().Services[0]
	if _, err := ResolveEnv(svc, nil); err == nil {
		t.Fatal("expected error when required env var TZ has no value and no default")
	}
}
