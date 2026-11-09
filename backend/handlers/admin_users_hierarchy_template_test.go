package handlers

import (
	"strings"
	"testing"
)

func TestAdminUsersPageExplainsAndRendersAccessHierarchy(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/accounts.html")
	if err != nil {
		t.Fatalf("read accounts template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`<h1>Users</h1>`,
		`id="tab-households"`,
		`class="hierarchy-guide"`,
		`Household</span><span class="hierarchy-level-tech">Account`,
		`Person</span><span class="hierarchy-level-tech">Profile`,
		`Device</span><span class="hierarchy-level-tech">Client`,
		`fetch(basePath + '/api/clients')`,
		`function renderHouseholds()`,
		`function renderPersonRow(profile, forceShowStale = false)`,
		`function renderDeviceRow(client, orphaned)`,
		`function reassignClient(clientId, newProfileId`,
		`function pingClient(clientId)`,
		`function showRenameClientModal(clientId)`,
		`function renameClient(e, clientId)`,
		`JSON.stringify({ nickname })`,
		`function deleteClient(clientId, clientName, profileId)`,
		`fromUserId: originalProfileId`,
		`const STALE_DEVICE_AGE_MS = 7 * 24 * 60 * 60 * 1000`,
		`function isClientStale(client)`,
		`function toggleStaleDevices(profileId)`,
		`const expandedHouseholds = new Set()`,
		`const expandedPeople = new Set()`,
		`function togglePerson(event, profileId)`,
		`not seen in 7+ days`,
		`Needs attention`,
		`client.nickname || client.name || 'Unknown device'`,
		`onclick="showRenameClientModal('${client.id}')"`,
		`${orphaned ? '<option value="" selected disabled>Move to person…</option>' : ''}`,
		`onchange="if (this.value) reassignClient(`,
		`onclick="showTransferAdminModal(${jsArg(a.id)}, ${jsArg(a.username)})"`,
		`function transferAdmin(event, targetAccountId)`,
		`/api/accounts/transfer-admin`,
		`Your account will lose admin access immediately`,
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("accounts template missing hierarchy marker %q", marker)
		}
	}

	if strings.Contains(source, `id="tab-accounts"`) || strings.Contains(source, `id="tab-profiles">Profiles</button>`) {
		t.Fatal("admin Users page still exposes separate Accounts or Profiles tabs")
	}
	if !strings.Contains(source, `const isExpanded = Boolean(search) || expandedHouseholds.has(a.id)`) ||
		!strings.Contains(source, `household-card${isExpanded ? '' : ' collapsed'}`) {
		t.Fatal("admin Users page households do not start collapsed when no search is active")
	}
	if !strings.Contains(source, `person-row${isExpanded ? '' : ' collapsed'}`) {
		t.Fatal("admin Users page people do not start collapsed")
	}
	if !strings.Contains(source, `const isExpanded = forceShowStale || expandedPeople.has(profile.id)`) {
		t.Fatal("admin Users page people do not expand to expose device search matches")
	}
}

func TestAdminUsesAutomationTerminologyAndAlignedFilters(t *testing.T) {
	toolsBytes, err := adminTemplates.ReadFile("admin_templates/tools.html")
	if err != nil {
		t.Fatalf("read tools template: %v", err)
	}
	baseBytes, err := adminTemplates.ReadFile("admin_templates/base.html")
	if err != nil {
		t.Fatalf("read base template: %v", err)
	}
	toolsSource := string(toolsBytes)
	baseSource := string(baseBytes)

	for _, marker := range []string{
		`<h1>Automations</h1>`,
		`Find an automation`,
		`Name, service, or automation type`,
		`Add Automation`,
		`id="taskSearchFilter" class="form-input" name="mediastorm-task-filter" type="search"`,
		`readonly aria-autocomplete="none"`,
		`data-bwignore="true" data-protonpass-ignore="true" data-form-type="other"`,
		`onpointerdown="activateTaskSearch(this)" onkeydown="activateTaskSearch(this)"`,
		`function handleTaskSearchInput(input)`,
		`.task-toolbar .form-input,`,
		`height: 2.5rem;`,
		`min-height: 2.5rem;`,
	} {
		if !strings.Contains(toolsSource, marker) {
			t.Fatalf("tools template missing automation UX marker %q", marker)
		}
	}
	if !strings.Contains(baseSource, "Automations") {
		t.Fatal("admin navigation does not use Automations terminology")
	}
}

func TestToolsPageOmitsObsoleteDeviceManagementNotice(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/tools.html")
	if err != nil {
		t.Fatalf("read tools template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`Device Management Has Moved`,
		`Devices now appear with their person and household`,
		`Open Households, People &amp; Devices`,
	} {
		if strings.Contains(source, marker) {
			t.Fatalf("tools template still contains obsolete device relocation marker %q", marker)
		}
	}

	if strings.Contains(source, `id="clientManagementSection"`) || strings.Contains(source, `id="clientsList"`) {
		t.Fatal("Tools page still renders the old client management interface")
	}
}

func TestAdminSettingsUsesHierarchyScopeTree(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/settings.html")
	if err != nil {
		t.Fatalf("read settings template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`id="settingsScopeTree" class="settings-scope-tree settings-scope-tree-storage"`,
		`class="settings-scope-bar"`,
		`id="settingsServerScopeBtn"`,
		`id="userSelector" aria-label="Person"`,
		`id="clientSelector" aria-label="Device"`,
		`function updateSettingsScopeBarState()`,
		`function renderSettingsScopeTree()`,
		`function renderSettingsPersonScope(profile)`,
		`function selectSettingsScope(kind, profileId, clientId = '')`,
		`function toggleSettingsServer(event)`,
		`function toggleSettingsPerson(event, profileId)`,
		`function expandSettingsScopeToProfile(profileId)`,
		`settingsScopeServerExpanded = true;`,
		`expandedSettingsHouseholds.add(householdId);`,
		`expandSettingsScopeToProfile(urlProfileId);`,
		`let settingsScopeServerExpanded = false;`,
		`class="settings-scope-server-line"`,
		`client.nickname || client.name || client.deviceName || 'Unknown device'`,
		`const STALE_SETTINGS_DEVICE_AGE_MS = 7 * 24 * 60 * 60 * 1000`,
		`function isSettingsDeviceStale(client)`,
		`not seen in 7+ days`,
		`loadSettingsScopeClients()`,
		`id="settingsSearch" class="form-input" placeholder="Search settings..." autocomplete="off"`,
		`readonly aria-autocomplete="none"`,
		`function handleSettingsSearchInput(input)`,
		`data-bwignore="true" data-protonpass-ignore="true" data-form-type="other"`,
		`clientSelector.disabled = !selectedUserId || clientsForUser.length === 0;`,
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("settings template missing hierarchy scope marker %q", marker)
		}
	}

	if strings.Contains(source, `<select id="userSelector" hidden`) ||
		strings.Contains(source, `<select id="clientSelector" hidden`) {
		t.Fatal("settings page hides the approved horizontal scope selectors")
	}
	if strings.Contains(source, `name="mediastorm-settings-filter"`) {
		t.Fatal("settings search retains a stable field name that autofill can target")
	}
	if strings.Contains(source, `const selectingCurrentPerson =`) {
		t.Fatal("selecting a person still toggles that person's device branch")
	}
	if strings.Contains(source, `households.forEach(account => expandedSettingsHouseholds.add(account.id))`) {
		t.Fatal("settings households are still expanded on initial render")
	}
	if strings.Contains(source, `.settings-scope-tree::-webkit-scrollbar`) ||
		strings.Contains(source, `max-height: calc(100vh - 92px)`) ||
		strings.Contains(source, `.settings-scope-tree { max-height:`) {
		t.Fatal("settings scope hierarchy still creates an internal scroll container")
	}
}

func TestAdminSettingsSurfacesAndReviewsScopedCustomizations(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/settings.html")
	if err != nil {
		t.Fatalf("read settings template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`class="settings-scope-custom"`,
		`function profileCustomizationCount(profileId)`,
		`function settingsScopeCustomizationBadge(count, label = 'custom')`,
		`Review Customizations`,
		`Review profile customizations`,
		`Review device customizations`,
		`Use Parent Defaults`,
		`function updateProfileSaveImpact(changedGroupKeys)`,
		`function updateClientSaveImpact()`,
		`class="settings-impact-banner"`,
		`Those custom values remain unchanged and continue to take precedence over server defaults.`,
		`Those device values remain unchanged and continue to take precedence over profile defaults.`,
		`if (selectedClientId && clientSettings !== null)`,
		`function clientSettingsUrl(clientId, userId)`,
		`showToast('Device settings saved successfully')`,
		`function settingsSectionCustomizationCount(sectionKey)`,
		`function settingsGroupCustomizationCount(groupId)`,
		`function buildProfileOverrideDetails(settings)`,
		`function hasOwnAtPath(obj, path)`,
		`{ key: 'ranking', groupKey: 'ranking' }`,
		`Object.entries(schema[sec.key]?.fields || {})`,
		`nestedDef.parent !== sec.key || nestedDef.is_array || nestedDef.group`,
		`details[groupKey].push('Live TV Sources')`,
		`if (fieldKey === 'useLoadingScreen') return 'Loading Screen'`,
		`return (buildProfileOverrideDetails(userSettings).ranking || []).length`,
		`filtering: 'contentFiltering'`,
		`return (buildProfileOverrideDetails(userSettings)[groupKey] || []).length`,
		`Array.isArray(profileShelves) && profileShelves.length > 0 && !shelvesEqual(profileShelves, globalShelves)`,
		`const details = buildProfileOverrideDetails(settings);`,
		`const fields = details[group.key];`,
		`function profileComparisonPathsForGroup(groupKey)`,
		`for (const userPath of profileComparisonPathsForGroup(group.key))`,
		`contentFiltering: 'filtering'`,
		`for (const fieldKey of liveTVPerUserFields)`,
		`delete targetSettings[sectionKey]`,
		`'liveTV', 'display', 'network', 'ranking', 'calendar'`,
		`class="settings-customization-count`,
		`renderCustomizationCount(customizationCount, 'section-customization-count')`,
		`['debrid.criteria', 'Debrid Ranking Criteria']`,
		`['usenet.criteria', 'Usenet Ranking Criteria']`,
		`rankingCriteria: 'Overall Ranking Criteria'`,
		`function clientRankingKeyForPath(criteriaPath)`,
		`function getSchemaZeroDefault(path)`,
		`encodeURIComponent(userId) + '&raw=true'`,
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("settings template missing scoped-customization marker %q", marker)
		}
	}

	if strings.Contains(source, `Copy to Profiles`) || strings.Contains(source, `Copy to Devices`) {
		t.Fatal("settings page still describes resetting child overrides as copying settings")
	}
	if strings.Contains(source, `fetch(basePath + '/api/settings/propagate'`) {
		t.Fatal("settings page still invokes the legacy bulk propagation endpoint")
	}
	if strings.Contains(source, `for (const path of group.userPaths) {
            deleteAtPath(targetSettings, path);`) {
		t.Fatal("profile reset still relies on an incomplete hand-maintained field list")
	}
}
