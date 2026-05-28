package unattend

import (
	"bytes"
	"fmt"
	"html"
	"strings"
)

// AgentInstallCommand is the synchronous first-logon command that bootstraps
// the AutoDeploy agent on the freshly imaged machine. It is appended to the
// operator's first-logon list automatically by Generate.
const AgentInstallCommand = `cmd /c start /b "" \\Windows\\AutoDeploy\\autodeploy-agent.exe --server %AUTODEPLOY_SERVER%`

// Generate writes a complete Windows 10/11 unattend.xml for s.
//
// The generated XML covers three passes:
//
//   windowsPE       — language/keyboard/disk/edition (for Windows Setup,
//                     even though AutoDeploy applies images directly with
//                     wimlib; some passes still run during specialize.)
//   specialize      — computer name, time zone, domain join.
//   oobeSystem      — OOBE skip flags, local admin, first-logon commands.
func Generate(s Settings) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">` + "\n")

	writeWindowsPE(&b, s)
	writeSpecialize(&b, s)
	writeOOBE(&b, s)

	b.WriteString(`</unattend>` + "\n")
	return b.Bytes(), nil
}

func writeWindowsPE(b *bytes.Buffer, s Settings) {
	fmt.Fprint(b, `  <settings pass="windowsPE">
    <component name="Microsoft-Windows-International-Core-WinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <SetupUILanguage><UILanguage>`+esc(s.UILanguage)+`</UILanguage></SetupUILanguage>
      <InputLocale>`+esc(s.Keyboard)+`</InputLocale>
      <SystemLocale>`+esc(s.Locale)+`</SystemLocale>
      <UILanguage>`+esc(s.UILanguage)+`</UILanguage>
      <UserLocale>`+esc(s.Locale)+`</UserLocale>
    </component>
    <component name="Microsoft-Windows-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <UserData>
        <AcceptEula>true</AcceptEula>
`)
	if s.ProductKey != "" {
		fmt.Fprintf(b, `        <ProductKey><Key>%s</Key><WillShowUI>OnError</WillShowUI></ProductKey>`+"\n", esc(s.ProductKey))
	}
	fmt.Fprint(b, `      </UserData>
    </component>
  </settings>
`)
}

func writeSpecialize(b *bytes.Buffer, s Settings) {
	fmt.Fprint(b, `  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
`)
	switch s.NameStrategy {
	case "literal":
		fmt.Fprintf(b, `      <ComputerName>%s</ComputerName>`+"\n", esc(s.ComputerName))
	case "prefix":
		// "*" means Windows will use the machine's UUID-based default; we
		// can't compute the serial here, so the agent renames the machine
		// authoritatively post-deploy (Phase 13 bulk rename). Use a
		// prefix-* hint so the initial Windows name is identifiable.
		fmt.Fprintf(b, `      <ComputerName>%s*</ComputerName>`+"\n", esc(s.ComputerName))
	default: // "random"
		fmt.Fprint(b, `      <ComputerName>*</ComputerName>`+"\n")
	}
	fmt.Fprintf(b, `      <TimeZone>%s</TimeZone>`+"\n", esc(s.TimeZone))
	fmt.Fprint(b, `    </component>
`)
	if s.DomainJoin != nil {
		writeDomainJoin(b, s.DomainJoin)
	}
	fmt.Fprint(b, `  </settings>
`)
}

func writeDomainJoin(b *bytes.Buffer, d *DomainJoin) {
	fmt.Fprint(b, `    <component name="Microsoft-Windows-UnattendedJoin" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <Identification>
        <Credentials>
          <Domain>`+esc(d.Domain)+`</Domain>
          <Username>`+esc(d.JoinUser)+`</Username>
          <Password>`+esc(d.JoinPassword)+`</Password>
        </Credentials>
        <JoinDomain>`+esc(d.Domain)+`</JoinDomain>
`)
	if d.OU != "" {
		fmt.Fprintf(b, `        <MachineObjectOU>%s</MachineObjectOU>`+"\n", esc(d.OU))
	}
	fmt.Fprint(b, `      </Identification>
    </component>
`)
}

func writeOOBE(b *bytes.Buffer, s Settings) {
	fmt.Fprint(b, `  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE>
        <HideEULAPage>`+boolStr(s.HideEULA)+`</HideEULAPage>
        <HideOEMRegistrationScreen>`+boolStr(s.HideOEMRegistration)+`</HideOEMRegistrationScreen>
        <HideOnlineAccountScreens>`+boolStr(s.HideOnlineAccountScreens)+`</HideOnlineAccountScreens>
        <HideWirelessSetupInOOBE>`+boolStr(s.HideWirelessSetup)+`</HideWirelessSetupInOOBE>
        <ProtectYourPC>`+fmt.Sprint(s.ProtectYourPC)+`</ProtectYourPC>
`)
	if s.SkipMachineOOBE {
		fmt.Fprint(b, `        <SkipMachineOOBE>true</SkipMachineOOBE>`+"\n")
	}
	if s.SkipUserOOBE {
		fmt.Fprint(b, `        <SkipUserOOBE>true</SkipUserOOBE>`+"\n")
	}
	fmt.Fprint(b, `      </OOBE>
`)
	if !s.HideAdmin && s.AdminUser != "" {
		fmt.Fprintf(b, `      <UserAccounts>
        <AdministratorPassword>
          <Value>%s</Value>
          <PlainText>true</PlainText>
        </AdministratorPassword>
      </UserAccounts>
`, esc(s.AdminPassword))
	}
	// First-logon: sort operator commands by Order, then append the
	// AutoDeploy agent bootstrap so it always runs after operator setup.
	cmds := append([]FirstLogonCommand(nil), s.FirstLogonCommands...)
	sortByOrder(cmds)
	cmds = append(cmds, FirstLogonCommand{
		Order:       lastOrder(cmds) + 10,
		Description: "Start AutoDeploy agent",
		CommandLine: AgentInstallCommand,
	})
	fmt.Fprint(b, `      <FirstLogonCommands>
`)
	for i, c := range cmds {
		fmt.Fprintf(b, `        <SynchronousCommand wcm:action="add">
          <Order>%d</Order>
          <Description>%s</Description>
          <CommandLine>%s</CommandLine>
        </SynchronousCommand>
`, i+1, esc(c.Description), esc(c.CommandLine))
	}
	fmt.Fprint(b, `      </FirstLogonCommands>
    </component>
    <component name="Microsoft-Windows-International-Core" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <InputLocale>`+esc(s.Keyboard)+`</InputLocale>
      <SystemLocale>`+esc(s.Locale)+`</SystemLocale>
      <UILanguage>`+esc(s.UILanguage)+`</UILanguage>
      <UserLocale>`+esc(s.Locale)+`</UserLocale>
    </component>
  </settings>
`)
}

func esc(s string) string { return html.EscapeString(s) }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func sortByOrder(cmds []FirstLogonCommand) {
	// Insertion sort; expected N is small (handful).
	for i := 1; i < len(cmds); i++ {
		for j := i; j > 0 && cmds[j-1].Order > cmds[j].Order; j-- {
			cmds[j-1], cmds[j] = cmds[j], cmds[j-1]
		}
	}
}

func lastOrder(cmds []FirstLogonCommand) int {
	if len(cmds) == 0 {
		return 0
	}
	return cmds[len(cmds)-1].Order
}

// XMLContainsSecret reports whether x mentions one of the supplied secret
// values literally. Tests use this to assert that generated XML really did
// carry the password (necessary for the OS to install) while logs do not.
func XMLContainsSecret(x []byte, secret string) bool {
	if secret == "" {
		return false
	}
	return bytes.Contains(x, []byte(secret)) ||
		strings.Contains(string(x), esc(secret))
}
