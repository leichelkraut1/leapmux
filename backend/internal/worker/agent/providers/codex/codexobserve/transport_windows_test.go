//go:build windows

package codexobserve

import (
 "testing"
 "golang.org/x/sys/windows"
 "github.com/stretchr/testify/require"
)

func TestVerifyWindowsACL(t *testing.T) {
 const owner="S-1-5-21-111-222-333-1001"
 sid,err:=windows.StringToSid(owner)
 require.NoError(t,err)
 tests:=[]struct{name,sddl string;allowed bool}{
  {"owner only","O:"+owner+"G:"+owner+"D:P(A;;FA;;;"+owner+")",true},
  {"normal user profile","O:"+owner+"G:"+owner+"D:P(A;;FA;;;"+owner+")(A;;FA;;;SY)(A;;FA;;;BA)",true},
  {"other users read only","O:"+owner+"G:"+owner+"D:P(A;;FA;;;"+owner+")(A;;GR;;;WD)",true},
  {"other users may write","O:"+owner+"G:"+owner+"D:P(A;;FA;;;"+owner+")(A;;GW;;;WD)",false},
  {"other users may change ACL","O:"+owner+"G:"+owner+"D:P(A;;FA;;;"+owner+")(A;;WD;;;WD)",false},
  {"different owner","O:SYG:SYD:P(A;;FA;;;"+owner+")",false},
  {"null ACL","O:"+owner+"G:"+owner+"D:NO_ACCESS_CONTROL",false},
  {"empty ACL","O:"+owner+"G:"+owner+"D:P",false},
 }
 for _,tt:=range tests {
  t.Run(tt.name,func(t *testing.T){
   sd,err:=windows.SecurityDescriptorFromString(tt.sddl)
   require.NoError(t,err)
   if tt.allowed { require.NoError(t,verifyWindowsACL(sd,sid)) } else { require.Error(t,verifyWindowsACL(sd,sid)) }
  })
 }
}
